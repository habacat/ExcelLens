package server

import (
	"context"

	"net/http"
	"sync"
	"sync/atomic"

	"excellens/internal/i18n"
	"excellens/internal/localstore"
)

// A replay must also be available when the transfer-history file is unreadable.
type recoveryMode struct {
	reason   atomic.Pointer[error]
	mu       sync.Mutex
	startup  bool
	running  bool
	closed   bool
	done     chan struct{}
	progress localstore.Progress
}

func (s *recoveryMode) set(err error) {
	s.reason.Store(&err)
}
func (s *recoveryMode) pending() bool { return s.reason.Load() != nil }

func (a *App) handleRecoveryMode(w http.ResponseWriter, r *http.Request) {
	roots, err := dataRoots()
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if err := a.startRecovery(roots, r.Method == http.MethodPost); err != nil {
		writeFailure(w, 409, err)
		return
	}
	s := &a.restoreState
	s.mu.Lock()
	reason := ""
	if msg := s.reason.Load(); msg != nil {
		reason = responseLocale(w).ErrorText(*msg)
	}
	progress := s.progress.Localized(responseLocale(w))
	state := map[string]any{"pending": s.pending(), "reason": reason, "running": s.running, "roots": roots, "progress": progress}
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
		return i18n.Error("service.storageRecovery.programExiting")
	}
	if !a.storageMu.TryLock() {
		return i18n.Error("service.storageRecovery.anotherDataManagementOperationProgress")
	}
	s.startup = false
	s.running = true
	s.done = make(chan struct{})
	s.progress = localstore.ProgressForStage("stage.task.readingTheRecoveryTransaction")
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
	a.locale.invalidate()
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
