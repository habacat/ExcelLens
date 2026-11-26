package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"excellens/internal/localstore"
)

// A replay must also be available when the transfer-history file is unreadable.
type recoveryMode struct {
	reason   atomic.Pointer[string]
	mu       sync.Mutex
	startup  bool
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
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if err := a.startRecovery(roots, r.Method == http.MethodPost); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	s := &a.restoreState
	s.mu.Lock()
	reason := ""
	if msg := s.reason.Load(); msg != nil {
		reason = *msg
	}
	state := map[string]any{"pending": s.pending(), "reason": reason, "running": s.running, "roots": roots, "progress": s.progress}
	s.mu.Unlock()
	writeJSON(w, state)
}

func (a *App) startRecovery(roots localstore.Roots, retry bool) error {
	s := &a.restoreState
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending() || s.running || (!retry && !s.startup) {
		return nil
	}
	if s.closed {
		return errors.New("程序正在退出")
	}
	if !a.storageMu.TryLock() {
		return errors.New("另一项数据管理操作正在进行")
	}
	s.startup = false
	s.running = true
	s.done = make(chan struct{})
	s.progress = localstore.Progress{Phase: "正在读取恢复事务"}
	go a.retryRecovery(roots)
	return nil
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
	if err == nil {
		recoverSnapshotStages(roots)
	}
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
