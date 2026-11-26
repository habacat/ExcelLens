package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"excellens/internal/browserlease"
	"excellens/internal/workbook"
)

// Each browser tab has an independent application state: workbook, indexes,
// filters, searches, exports and analysis jobs. Persistent caches and recent
// files are intentionally shared on disk; opening a second workbook must never
// replace the first tab's in-memory workbook or cancel its jobs.
const maxBrowserSessions = 64

type browserSession struct {
	app    *App
	routes http.Handler
	lease  browserlease.Lease
	work   *browserlease.Work
	saved  *backgroundResults
}

func validSessionID(id string) bool {
	if len(id) < 20 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

var (
	errSessionExpired = errors.New("页面会话已过期，请刷新页面并重新打开文件")
	errPageReplaced   = errors.New("此页面的会话已由其他页面接管，请刷新页面")
)

type sessionAccess struct {
	page         string
	generation   uint64 // nonzero only on an explicit attach
	background   bool
	existingOnly bool
}

// The caller owns one request until it releases the returned session. This
// includes native opens, uploads and streaming downloads.
func (a *App) acquireSession(id, launchPath string, access sessionAccess) (*browserSession, error) {
	if !validSessionID(id) {
		return nil, errors.New("无效的页面会话标识")
	}
	a.sessionGate.RLock()
	defer a.sessionGate.RUnlock()
	a.sessionsMu.Lock()
	var expired []*browserSession
	defer func() { a.sessionsMu.Unlock(); a.finishRetirement(expired) }()
	now := time.Now()
	s := a.sessions[id]
	if s == nil || s.lease.Expired(now) && !s.retained(now) {
		expired = a.takeExpiredLocked(now)
		if access.existingOnly {
			return nil, errSessionExpired
		}
		if a.pageCountLocked(now) >= maxBrowserSessions {
			return nil, fmt.Errorf("同时打开的页面已达 %d 个，请关闭旧页面并稍后重试", maxBrowserSessions)
		}
		if len(a.sessions) >= 2*maxBrowserSessions {
			var oldest *browserSession
			var oldestID string
			for key, other := range a.sessions {
				running, _ := other.work.State(now)
				if !running && other.lease.Idle(now) && (oldest == nil || other.lease.Deadline().Before(oldest.lease.Deadline())) {
					oldest, oldestID = other, key
				}
			}
			if oldest != nil {
				delete(a.sessions, oldestID)
				if oldest.app != nil {
					a.retiring[oldest.app] = struct{}{}
				}
				expired = append(expired, oldest)
			}
		}
		var err error
		s, err = a.newBrowserSession(launchPath)
		if err != nil {
			return nil, err
		}
		a.sessions[id] = s
	}
	if s.lease.Idle(now) && s.retained(now) && access.generation != 0 && a.pageCountLocked(now) >= maxBrowserSessions {
		return nil, fmt.Errorf("同时打开的页面已达 %d 个", maxBrowserSessions)
	}
	if s.saved != nil && a.restoreActive.Load() {
		return nil, errors.New("迁入备份进行中，请稍后查看结果")
	}
	if access.background && !s.lease.Unattended(now) {
		return nil, errPageReplaced
	}
	if access.generation != 0 {
		if !s.lease.Attach(access.page, access.generation, now) {
			return nil, errPageReplaced
		}
	} else if access.page != "" && !s.lease.Owns(access.page) {
		return nil, errPageReplaced
	}
	if s.saved != nil {
		fresh, err := a.newBrowserSession("")
		if err != nil {
			return nil, err
		}
		s.saved.attach(fresh.app)
		fresh.app.submitted = s.work
		s.saved, s.app, s.routes = nil, fresh.app, fresh.routes
	}
	s.lease.Begin(now)
	return s, nil
}

func (a *App) releaseSession(s *browserSession) {
	a.sessionsMu.Lock()
	defer a.sessionsMu.Unlock()
	s.lease.End(time.Now())
}

func (a *App) newBrowserSession(launchPath string) (*browserSession, error) {
	child, err := newApp()
	if err != nil {
		return nil, err
	}
	child.owner = a
	child.cacheMu = a.cacheMu
	child.workAdmission = a.workAdmission
	child.edits.revision = a.edits.revision
	routes := child.routes()
	if launchPath != "" {
		child.launchPath = launchPath
		next := routes
		open := sync.OnceFunc(func() {
			defer func() { child.mu.Lock(); child.launchPath = ""; child.mu.Unlock() }()
			if err := child.OpenPath(launchPath); err != nil {
				child.mu.Lock()
				child.startupError = err.Error()
				child.mu.Unlock()
			}
		})
		routes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			open()
			next.ServeHTTP(w, r)
		})
	}
	return &browserSession{app: child, routes: routes, work: child.submitted}, nil
}

func (s *browserSession) retained(now time.Time) bool {
	_, retained := s.work.State(now)
	return retained
}

func (a *App) pageCountLocked(now time.Time) int {
	count := 0
	for _, s := range a.sessions {
		if s.saved == nil && !(s.lease.Idle(now) && s.retained(now)) {
			count++
		}
	}
	return count
}

func (a *App) takeExpiredLocked(now time.Time) []*browserSession {
	var expired []*browserSession
	for id, s := range a.sessions {
		if s.lease.Expired(now) && !s.retained(now) {
			expired = append(expired, s)
			if s.app != nil {
				a.retiring[s.app] = struct{}{}
			}
			delete(a.sessions, id)
		}
	}
	return expired
}

func (a *App) finishRetirement(expired []*browserSession) {
	for _, child := range expired {
		child.close()
		a.sessionsMu.Lock()
		delete(a.retiring, child.app)
		a.sessionsMu.Unlock()
	}
}

func (a *App) runSessionReaper(stop <-chan struct{}, ticks <-chan time.Time) {
	for {
		select {
		case <-stop:
			return
		case <-ticks:
			a.cacheResetMu.RLock()
			a.sessionGate.Lock()
			a.parkUnattended(false, "")
			a.sessionGate.Unlock()
			a.cacheResetMu.RUnlock()
		}
	}
}

// PreparePathForLaunch lets the browser paint before the first API request
// reads the native file. Only the new tab waits, outside the sessions lock.
func (a *App) PreparePathForLaunch(path string) (string, error) {
	if a.restoreState.pending() {
		return "", errors.New("请先完成恢复事务")
	}
	a.cacheResetMu.RLock()
	defer a.cacheResetMu.RUnlock()
	if a.restoreState.pending() {
		return "", errors.New("请先完成恢复事务")
	}
	id := randomID()
	s, err := a.acquireSession(id, path, sessionAccess{})
	if err != nil {
		return "", err
	}
	a.releaseSession(s)
	return id, nil
}

// A native shell context-menu launch explicitly supplies a workbook. It opens
// in a NEW tab session rather than changing whichever browser tab was active.
// A normal app/tray launch supplies no workbook and always opens the home page.
func (a *App) OpenPathForLaunch(path string) (string, error) {
	if a.restoreState.pending() {
		return "", errors.New("请先完成恢复事务")
	}
	a.cacheResetMu.RLock()
	defer a.cacheResetMu.RUnlock()
	if a.restoreState.pending() {
		return "", errors.New("请先完成恢复事务")
	}
	id := randomID()
	session, err := a.acquireSession(id, "", sessionAccess{})
	if err != nil {
		return "", err
	}
	defer a.releaseSession(session)
	if err := session.app.OpenPath(path); err != nil {
		a.sessionsMu.Lock()
		delete(a.sessions, id)
		a.sessionsMu.Unlock()
		session.app.Close()
		return "", err
	}
	return id, nil
}

// Called after the outer instance has validated the loopback address and API
// token. No session credentials are accepted from the workbook itself.
func (a *App) routeSessions(defaultRoutes http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			defaultRoutes.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/storage/recovery" {
			a.handleRecoveryMode(w, r)
			return
		}
		if a.restoreState.pending() {
			if r.URL.Path == "/api/storage" && r.Method == http.MethodGet {
				a.handleStorage(w, r)
				return
			}
			w.Header().Set("X-ExcelLens-Recovery", "required")
			if r.URL.Path == "/api/exit" {
				a.handleExit(w, r)
				return
			}
			writeError(w, 503, "恢复事务尚未完成，请打开本地数据")
			return
		}
		if r.URL.Path == "/api/background" {
			a.handleBackground(w, r)
			return
		}
		if r.URL.Path == "/api/database/snapshots/removal" {
			a.handleSnapshotRemoval(w, r)
			return
		}
		if r.URL.Path == "/api/storage/jobs" {
			a.handleStorageJobs(w, r)
			return
		}
		if r.URL.Path == "/api/storage/sources" {
			a.handleSources(w, r)
			return
		}
		if r.URL.Path == "/api/storage" {
			a.handleStorage(w, r)
			return
		}
		// Interface watch and lease control must not hold cacheResetMu while waiting.
		if r.URL.Path == "/api/interface" {
			defaultRoutes.ServeHTTP(w, r)
			return
		}
		// Lease control stays responsive while restore has exclusive data access.
		if !strings.HasPrefix(r.URL.Path, "/api/session/") {
			a.cacheResetMu.RLock()
			defer a.cacheResetMu.RUnlock()
			if a.restoreState.pending() {
				w.Header().Set("X-ExcelLens-Recovery", "required")
				writeError(w, 503, "恢复事务尚未完成，请打开本地数据")
				return
			}
		}
		if epoch := r.Header.Get("X-ExcelLens-Cache-Epoch"); epoch != "" && epoch != strconv.FormatUint(a.cacheEpoch.Load(), 10) {
			writeError(w, http.StatusConflict, "全部缓存已清理，请返回首页重新打开文件")
			return
		}
		id := r.Header.Get("X-ExcelLens-Session")
		// <img> and download anchors cannot set custom headers. Those two
		// endpoints require their existing unguessable media/download tickets.
		ticket := id == "" && (r.URL.Path == "/api/image/content" || r.URL.Path == "/api/export/download" || r.URL.Path == "/api/search/export")
		if ticket {
			id = r.URL.Query().Get("session")
		}
		page := r.Header.Get("X-ExcelLens-Page")
		control := strings.HasPrefix(r.URL.Path, "/api/session/")
		access := sessionAccess{page: page, existingOnly: ticket || page != ""}
		if control {
			if r.Method != http.MethodPost {
				methodNotAllowed(w)
				return
			}
			if !validSessionID(id) || !validSessionID(page) {
				writeError(w, 400, "页面租约标识无效")
				return
			}
			switch r.URL.Path {
			case "/api/session/attach":
				gen, err := strconv.ParseUint(r.Header.Get("X-ExcelLens-Generation"), 10, 53)
				if err != nil || gen == 0 {
					writeError(w, 400, "页面租约序号无效")
					return
				}
				access.background = r.Header.Get("X-ExcelLens-Background") == "1"
				access.generation, access.existingOnly = gen, access.background || r.Header.Get("X-ExcelLens-Resume") == "1"
			case "/api/session/heartbeat":
			case "/api/session/detach":
				a.sessionsMu.Lock()
				if s := a.sessions[id]; s != nil {
					s.lease.Detach(page, time.Now())
				}
				a.sessionsMu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			default:
				http.NotFound(w, r)
				return
			}
		}
		if id == "" { // compatibility with local API clients and existing tests
			defaultRoutes.ServeHTTP(w, r)
			return
		}
		session, err := a.acquireSession(id, "", access)
		if err != nil {
			status := http.StatusTooManyRequests
			if errors.Is(err, errSessionExpired) {
				status = http.StatusGone
			} else if errors.Is(err, errPageReplaced) {
				status = http.StatusConflict
			}
			writeError(w, status, err.Error())
			return
		}
		defer a.releaseSession(session)
		if control {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		session.routes.ServeHTTP(w, r)
	})
}

func (a *App) closeBrowserSessions() {
	a.sessionGate.Lock()
	defer a.sessionGate.Unlock()
	a.sessionsMu.Lock()
	children := make([]*browserSession, 0, len(a.sessions))
	for id, session := range a.sessions {
		children = append(children, session)
		delete(a.sessions, id)
	}
	a.sessionsMu.Unlock()
	for _, child := range children {
		child.close()
	}
}

// Used by manual cleanup so it never deletes the cache/upload of a workbook
// being viewed in a different browser tab.
func (a *App) activeBrowserWorkbooks() []*App {
	a.sessionsMu.Lock()
	defer a.sessionsMu.Unlock()
	apps := []*App{a}
	for child := range a.retiring {
		apps = append(apps, child)
	}
	for _, s := range a.sessions {
		if s.app != nil {
			apps = append(apps, s.app)
		}
	}
	return apps
}

func (a *App) rootApp() *App {
	if a.owner != nil {
		return a.owner
	}
	return a
}

func (a *App) otherTabUsesCache(target string) bool {
	root := a.rootApp()
	if root.sourcePins.Any(func(wb *workbook.Workbook) bool { return wb.CacheDir == target }) {
		return true
	}
	for _, other := range root.activeBrowserWorkbooks() {
		if other == a {
			continue
		}
		if wb := other.currentWorkbook(); wb != nil && wb.CacheDir == target {
			return true
		}
	}
	return false
}
