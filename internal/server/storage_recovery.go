package server

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"excellens/internal/localstore"
)

// A replay must also be available when the transfer-history file is unreadable.
type recoveryMode struct {
	reason   atomic.Pointer[string]
	mu       sync.Mutex
	running  bool
	closed   bool
	done     chan struct{}
	progress localstore.Progress
}

func (s *recoveryMode) set(err error) {
	message := err.Error()
	s.reason.Store(&message)
}
func (s *recoveryMode) pending() bool { return s.reason.Load() != nil }

func (a *App) handleRecoveryMode(w http.ResponseWriter, r *http.Request) {
	roots, err := dataRoots()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s := &a.restoreState
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodPost {
		if !s.pending() {
			writeJSON(w, map[string]bool{"pending": false})
			return
		}
		if s.closed {
			writeError(w, 503, "程序正在退出")
			return
		}
		if !s.running {
			if !a.storageMu.TryLock() {
				writeError(w, 409, "另一项数据管理操作正在进行")
				return
			}
			s.running = true
			s.done = make(chan struct{})
			s.progress = localstore.Progress{Phase: "继续恢复事务"}
			go a.retryRecovery(roots)
		}
	} else if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	reason := ""
	if msg := s.reason.Load(); msg != nil {
		reason = *msg
	}
	writeJSON(w, map[string]any{"pending": s.pending(), "reason": reason, "running": s.running, "roots": roots, "progress": s.progress})
}

func (a *App) retryRecovery(roots localstore.Roots) {
	defer a.storageMu.Unlock()
	a.storageView.Lock()
	defer a.storageView.Unlock()
	a.cacheResetMu.Lock()
	defer a.cacheResetMu.Unlock()
	s := &a.restoreState
	report := func(p localstore.Progress) { s.mu.Lock(); s.progress = p; s.mu.Unlock() }
	err := roots.RecoverWith(context.Background(), localstore.TransferOptions{Report: report})
	a.toolbar.mu.Lock()
	a.toolbar.loaded = false
	a.toolbar.mu.Unlock()
	s.mu.Lock()
	if err != nil {
		s.set(err)
	} else {
		s.reason.Store(nil)
	}
	s.running = false
	close(s.done)
	s.done = nil
	s.mu.Unlock()
	a.toolbar.notify()
}

func (s *recoveryMode) stop() {
	s.mu.Lock()
	s.closed = true
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}
