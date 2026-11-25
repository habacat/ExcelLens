package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"excellens/internal/browserlease"
	"excellens/internal/recovery"
	"excellens/internal/transferjobs"
	"excellens/internal/workbook"
)

type App struct {
	submitted *browserlease.Work
	workAdmission
	queryController
	workbookSession
	databaseImportController
	edits            editController
	recoveryOnce     sync.Once
	recoveryStore    *recovery.Store
	recoveryErr      error
	recoverySem      chan struct{}
	archiveMu        sync.Mutex
	archiveView      *archiveView
	archiveTask      *archiveTask
	cacheMu          *sync.RWMutex // shared by all browser sessions and source pins
	transfers        transferjobs.Manager
	restoreState     recoveryMode
	restoreActive    atomic.Bool
	storageView      sync.RWMutex // storage readers and the worker changing their inputs
	storageMu        sync.Mutex   // root only: one explicit storage operation at a time
	cacheResetMu     sync.RWMutex // root only: drain requests before discarding active caches
	cacheEpoch       atomic.Uint64
	toolbar          toolbarPreference
	sourceMu         sync.Mutex
	sourcePins       map[*workbook.Workbook]int
	sourceUploads    map[string]int
	compositionBytes int64 // root sourceMu; shared across every browser session
	relationBytes    int64 // root sourceMu; retained inputs and independent plans
	reconcileMu      sync.Mutex
	reconcileBooks   map[string]*reconcileAttachment
	reconcileJobs    map[string]*reconcileTask
	recipeMu         sync.Mutex
	recipeCaptures   map[string]*recipeCaptureTask
	viewMu           sync.Mutex
	bookmarkMu       sync.Mutex
	recentMu         sync.Mutex

	analysisMu       sync.Mutex
	analysisJobs     map[string]*analysisTask
	analysisTreeJobs map[string]*analysisTreeTask
	analysisSources  map[string]*analysisSourceSnapshot
	catalogueJobs    map[string]*catalogueTask
	dataRuleJobs     map[string]*dataRuleTask
	printJobs        map[string]*printTask
	clusterJobs      map[string]*clusterTask
	compositionJobs  map[string]*compositionTask
	relationJobs     map[string]*relationTask
	exports          exportController

	server       *http.Server
	token        string
	mediaTicket  string
	shutdownOnce sync.Once
	sessionGate  sync.RWMutex // serializes result handoff with new page acquisitions
	sessionsMu   sync.Mutex
	sessions     map[string]*browserSession
	retiring     map[*App]struct{}
	owner        *App
}

func New() (*App, error) {
	a, err := newApp()
	if err != nil {
		return nil, err
	}
	roots, err := dataRoots()
	if err != nil {
		return nil, err
	}
	pending, err := roots.RecoveryPending()
	if err != nil {
		a.restoreState.set(err)
		log.Printf("已进入受限恢复模式：%v", err)
	} else if pending {
		a.restoreState.set(errors.New("检测到未完成的恢复事务"))
		a.restoreState.startup = true
	}
	return a, nil
}

func newApp() (*App, error) {
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, fmt.Errorf("生成本机访问令牌失败：%w", err)
	}
	var mediaBytes [32]byte
	if _, err := rand.Read(mediaBytes[:]); err != nil {
		return nil, fmt.Errorf("生成图片访问票据失败：%w", err)
	}
	return &App{
		submitted:       &browserlease.Work{},
		workAdmission:   newWorkAdmission(),
		queryController: newQueryController(),
		edits:           newEditController(),
		cacheMu:         &sync.RWMutex{},
		sourcePins:      make(map[*workbook.Workbook]int),
		sourceUploads:   make(map[string]int),
		reconcileBooks:  make(map[string]*reconcileAttachment),
		reconcileJobs:   make(map[string]*reconcileTask),
		recipeCaptures:  make(map[string]*recipeCaptureTask),
		recoverySem:     make(chan struct{}, 2),
		catalogueJobs:   make(map[string]*catalogueTask),
		dataRuleJobs:    make(map[string]*dataRuleTask),
		printJobs:       make(map[string]*printTask),
		clusterJobs:     make(map[string]*clusterTask),
		compositionJobs: make(map[string]*compositionTask),
		relationJobs:    make(map[string]*relationTask),
		exports:         make(exportController),

		token:       hex.EncodeToString(tokenBytes[:]),
		mediaTicket: hex.EncodeToString(mediaBytes[:]),

		analysisJobs:     make(map[string]*analysisTask),
		analysisTreeJobs: make(map[string]*analysisTreeTask),
		analysisSources:  make(map[string]*analysisSourceSnapshot),
		sessions:         make(map[string]*browserSession),
		retiring:         make(map[*App]struct{}),
	}, nil
}

func (a *App) Handler() http.Handler {
	return a.securityHeaders(a.routeSessions(a.routes()))
}

func (a *App) Listen() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("启动本机服务失败：%w", err)
	}
	a.server = &http.Server{
		DisableGeneralOptionsHandler: true,
		Handler:                      a.Handler(),
		ReadHeaderTimeout:            10 * time.Second,
		ReadTimeout:                  10 * time.Minute,
		WriteTimeout:                 10 * time.Minute,
		IdleTimeout:                  120 * time.Second,
		MaxHeaderBytes:               1 << 20,
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	return ln, url, nil
}

func (a *App) Serve(ln net.Listener) error {
	if a.server == nil {
		return errors.New("HTTP 服务尚未初始化")
	}
	if roots, err := dataRoots(); err == nil {
		a.startRecovery(roots, false)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	ticker := time.NewTicker(10 * time.Second)
	go func() { defer close(done); a.runSessionReaper(stop, ticker.C) }()
	err := a.server.Serve(ln)
	ticker.Stop()
	close(stop)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) Shutdown() {
	a.shutdownOnce.Do(func() {
		a.restoreState.stop()
		a.transfers.Stop()
		a.mu.Lock()
		for _, job := range a.catalogueJobs {
			job.cancel()
		}
		for _, job := range a.dataRuleJobs {
			job.cancel()
		}
		for _, job := range a.printJobs {
			job.cancel()
		}
		for _, job := range a.clusterJobs {
			job.cancel()
		}
		for _, job := range a.compositionJobs {
			job.cancel()
		}
		for _, job := range a.relationJobs {
			job.cancel()
		}
		a.queryController.cancelLocked()
		a.exports.cancelAllLocked()
		srv := a.server
		a.mu.Unlock()
		a.analysisMu.Lock()
		for _, job := range a.analysisJobs {
			job.cancel()
		}
		for _, job := range a.analysisTreeJobs {
			job.cancel()
		}
		a.analysisMu.Unlock()
		a.cancelReconciliations()
		a.cancelRecipeCaptures()
		if a.owner == nil {
			a.toolbar.shutdown()
		}
		if srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		}
	})
}

func (a *App) Close() {
	a.closeArchive()
	a.Shutdown()
	a.cacheResetMu.Lock()
	defer a.cacheResetMu.Unlock()
	a.closeDatabaseImport()
	a.closeBrowserSessions()
	a.replaceWorkbook(nil)
}

func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func ConfigureLog() func() {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return func() {}
	}
	dir := filepath.Join(base, "ExcelLens", "logs")
	if os.MkdirAll(dir, 0700) != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, "viewer.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return func() {}
	}
	log.SetOutput(f)
	return func() { _ = f.Close() }
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !remoteIsLoopback(r.RemoteAddr) || !localRequestAllowed(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/search/export" && r.URL.Path != "/api/export/download" && r.URL.Path != "/api/image/content" && r.URL.Path != "/api/edits/download" && r.Header.Get("X-ExcelLens-Token") != a.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// net/http supplies the socket address. Neither DNS nor proxy headers are
// trusted to identify this instance, including on routes with download tickets.
func localRequestAllowed(r *http.Request) bool {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok || local == nil || !local.IP.Equal(net.IPv4(127, 0, 0, 1)) || local.Port < 1 || local.Port > 65535 {
		return false
	}
	port := strconv.Itoa(local.Port)
	if r.Host != "127.0.0.1:"+port && r.Host != "localhost:"+port {
		return false
	}
	if origins := r.Header.Values("Origin"); len(origins) != 0 && (len(origins) != 1 || origins[0] != "http://"+r.Host) {
		return false
	}
	if refs := r.Header.Values("Referer"); len(refs) != 0 {
		ref, err := url.Parse(refs[0])
		if len(refs) != 1 || err != nil || ref.Scheme != "http" || ref.Host != r.Host || ref.User != nil {
			return false
		}
	}
	sites := r.Header.Values("Sec-Fetch-Site")
	return len(sites) == 0 || len(sites) == 1 && (sites[0] == "same-origin" || sites[0] == "none")
}

func remoteIsLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
