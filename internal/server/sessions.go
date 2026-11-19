package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Each browser tab has an independent application state: workbook, indexes,
// filters, searches, exports and analysis jobs. Persistent caches and recent
// files are intentionally shared on disk; opening a second workbook must never
// replace the first tab's in-memory workbook or cancel its jobs.
const maxBrowserSessions = 64
const sessionIdleLimit = 72 * time.Hour

type browserSession struct {
	app      *App
	routes   http.Handler
	lastSeen time.Time
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

func (a *App) sessionFor(id string) (*browserSession, error) {
	return a.sessionWithLaunch(id, "")
}

func (a *App) sessionWithLaunch(id, launchPath string) (*browserSession, error) {
	if !validSessionID(id) {
		return nil, errors.New("无效的页面会话标识")
	}
	now := time.Now()
	a.sessionsMu.Lock()
	if found := a.sessions[id]; found != nil {
		found.lastSeen = now
		a.sessionsMu.Unlock()
		return found, nil
	}
	var expired []*App
	for key, old := range a.sessions {
		if now.Sub(old.lastSeen) > sessionIdleLimit {
			expired = append(expired, old.app)
			delete(a.sessions, key)
		}
	}
	if len(a.sessions) >= maxBrowserSessions {
		a.sessionsMu.Unlock()
		for _, old := range expired {
			old.Close()
		}
		return nil, fmt.Errorf("同时打开的页面过多（最多 %d 个）；请关闭旧页面并重启 ExcelLens", maxBrowserSessions)
	}
	child, err := New()
	if err != nil {
		a.sessionsMu.Unlock()
		for _, old := range expired {
			old.Close()
		}
		return nil, err
	}
	child.owner = a
	child.cacheMu = a.cacheMu
	child.indexSem = a.indexSem
	child.querySem = a.querySem
	child.exportSem = a.exportSem
	child.analysisSem = a.analysisSem
	child.inspectSem = a.inspectSem
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
	session := &browserSession{app: child, routes: routes, lastSeen: now}
	a.sessions[id] = session
	a.sessionsMu.Unlock()
	for _, old := range expired {
		old.Close()
	}
	return session, nil
}

// PreparePathForLaunch lets the browser paint before the first API request
// reads the native file. Only the new tab waits, outside the sessions lock.
func (a *App) PreparePathForLaunch(path string) (string, error) {
	id := randomID()
	if _, err := a.sessionWithLaunch(id, path); err != nil {
		return "", err
	}
	return id, nil
}

// A native shell context-menu launch explicitly supplies a workbook. It opens
// in a NEW tab session rather than changing whichever browser tab was active.
// A normal app/tray launch supplies no workbook and always opens the home page.
func (a *App) OpenPathForLaunch(path string) (string, error) {
	id := randomID()
	session, err := a.sessionFor(id)
	if err != nil {
		return "", err
	}
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
		id := r.Header.Get("X-ExcelLens-Session")
		// <img> and download anchors cannot set custom headers. Those two
		// endpoints require their existing unguessable media/download tickets.
		if id == "" && (r.URL.Path == "/api/image/content" || r.URL.Path == "/api/export/download") {
			id = r.URL.Query().Get("session")
		}
		if id == "" { // compatibility with local API clients and existing tests
			defaultRoutes.ServeHTTP(w, r)
			return
		}
		session, err := a.sessionFor(id)
		if err != nil {
			writeError(w, http.StatusTooManyRequests, err.Error())
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
