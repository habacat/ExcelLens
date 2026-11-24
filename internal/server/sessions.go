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
	existingOnly bool
}

// The caller owns one request until it releases the returned session. This
// includes native opens, uploads and streaming downloads.
func (a *App) acquireSession(id, launchPath string, access sessionAccess) (*browserSession, error) {
	if !validSessionID(id) {
		return nil, errors.New("无效的页面会话标识")
	}
	a.sessionsMu.Lock()
	var expired []*App
	defer func() { a.sessionsMu.Unlock(); a.finishRetirement(expired) }()
	now := time.Now()
	s := a.sessions[id]
	if s == nil || s.lease.Expired(now) {
		expired = a.takeExpiredLocked(now)
		if access.existingOnly {
			return nil, errSessionExpired
		}
		if len(a.sessions) >= maxBrowserSessions {
			return nil, fmt.Errorf("同时打开的页面已达 %d 个，请关闭旧页面并稍后重试", maxBrowserSessions)
		}
		var err error
		s, err = a.newBrowserSession(launchPath)
		if err != nil {
			return nil, err
		}
		a.sessions[id] = s
	}
	if access.generation != 0 {
		if !s.lease.Attach(access.page, access.generation, now) {
			return nil, errPageReplaced
		}
	} else if access.page != "" && !s.lease.Owns(access.page) {
		return nil, errPageReplaced
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
	child, err := New()
	if err != nil {
		return nil, err
	}
	child.owner = a
	child.cacheMu = a.cacheMu
	child.workAdmission = a.workAdmission
	child.edits.revision = a.edits.revision
	routes := child.routes()
	if launchPath != "" {
		next := routes
		open := sync.OnceFunc(func() {
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
	return &browserSession{app: child, routes: routes}, nil
}

func (a *App) takeExpiredLocked(now time.Time) []*App {
	var expired []*App
	for id, s := range a.sessions {
		if s.lease.Expired(now) {
			expired = append(expired, s.app)
			a.retiring[s.app] = struct{}{}
			delete(a.sessions, id)
		}
	}
	return expired
}

func (a *App) finishRetirement(expired []*App) {
	for _, child := range expired {
		child.Close()
		a.sessionsMu.Lock()
		delete(a.retiring, child)
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
			a.sessionsMu.Lock()
			expired := a.takeExpiredLocked(time.Now())
			a.sessionsMu.Unlock()
			a.finishRetirement(expired)
			a.cacheResetMu.RUnlock()
		}
	}
}

// PreparePathForLaunch lets the browser paint before the first API request
// reads the native file. Only the new tab waits, outside the sessions lock.
func (a *App) PreparePathForLaunch(path string) (string, error) {
	a.cacheResetMu.RLock()
	defer a.cacheResetMu.RUnlock()
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
	a.cacheResetMu.RLock()
	defer a.cacheResetMu.RUnlock()
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
		if r.URL.Path == "/api/storage" {
			a.handleStorage(w, r)
			return
		}
		a.cacheResetMu.RLock()
		defer a.cacheResetMu.RUnlock()
		if r.URL.Path == "/api/interface" {
			defaultRoutes.ServeHTTP(w, r)
			return
		}
		if epoch := r.Header.Get("X-ExcelLens-Cache-Epoch"); epoch != "" && epoch != strconv.FormatUint(a.cacheEpoch.Load(), 10) {
			writeError(w, http.StatusConflict, "全部缓存已清理，请返回首页重新打开文件")
			return
		}
		id := r.Header.Get("X-ExcelLens-Session")
		// <img> and download anchors cannot set custom headers. Those two
		// endpoints require their existing unguessable media/download tickets.
		ticket := id == "" && (r.URL.Path == "/api/image/content" || r.URL.Path == "/api/export/download")
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
				access.generation, access.existingOnly = gen, r.Header.Get("X-ExcelLens-Resume") == "1"
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
	a.sessionsMu.Lock()
	children := make([]*App, 0, len(a.sessions))
	for id, session := range a.sessions {
		children = append(children, session.app)
		delete(a.sessions, id)
	}
	a.sessionsMu.Unlock()
	for _, child := range children {
		child.Close()
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
		apps = append(apps, s.app)
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
	root.sourceMu.Lock()
	for wb := range root.sourcePins {
		if wb.CacheDir == target {
			root.sourceMu.Unlock()
			return true
		}
	}
	root.sourceMu.Unlock()
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
