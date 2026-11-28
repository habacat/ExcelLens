package server

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func (a *App) retainSubmitted(id string, done <-chan struct{}) {
	if a.owner != nil {
		a.submitted.Track(id, done)
	}
}

type backgroundItem struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	File      string    `json:"file"`
	State     string    `json:"state"`
	Stage     string    `json:"stage"`
	Error     string    `json:"error,omitempty"`
	Created   time.Time `json:"created"`
	Session   string    `json:"session"`
	CanAttach bool      `json:"canAttach"`
	Protected bool      `json:"protected,omitempty"`
}

func (set backgroundResults) items(sid string, canAttach, idle bool) []backgroundItem {
	items := []backgroundItem{}
	add := func(id, kind, state, stage, msg string, t time.Time, wb *workbook.Workbook, protected bool) {
		if wb != nil {
			items = append(items, backgroundItem{id, kind, workbookDisplayName(wb), state, stage, msg, t, sid, canAttach, protected})
		}
	}
	for _, j := range set.exports {
		j.mu.RLock()
		add(j.ID, "export", j.Status, j.Stage, j.Error, j.Started, j.wb, false)
		j.mu.RUnlock()
	}
	for _, j := range set.searches {
		j.mu.RLock()
		add(j.ID, "search", j.Status, j.Stage, j.Error, j.Started, set.book, !idle && !canAttach && j.Results != nil)
		j.mu.RUnlock()
	}
	for _, j := range set.filters {
		j.mu.RLock()
		add(j.ID, "filter", j.Status, j.Stage, j.Error, j.Started, set.book, !idle && set.orders[j.ResultID] != nil)
		j.mu.RUnlock()
	}
	for _, j := range set.analyses {
		j.mu.RLock()
		add(j.ID, "analysis", j.Status, j.Stage, j.Error, j.Created, j.Workbook, false)
		j.mu.RUnlock()
	}
	for _, j := range set.comparisons {
		j.mu.RLock()
		add(j.ID, "reconcile", j.Status, j.Stage, j.Error, j.Created, j.Workbook, false)
		j.mu.RUnlock()
	}
	return items
}

type backgroundOwner struct {
	id           string
	session      *browserSession
	attach, idle bool
}

func (a *App) handleBackground(w http.ResponseWriter, r *http.Request) {
	// Workbook requests and background management use the same maintenance lock.
	if r.Method == http.MethodGet {
		a.cacheResetMu.RLock()
		defer a.cacheResetMu.RUnlock()
	} else {
		a.cacheResetMu.Lock()
		defer a.cacheResetMu.Unlock()
	}
	a.sessionGate.Lock()
	defer a.sessionGate.Unlock()
	if a.restoreState.pending() {
		writeMessage(w, 503, "service.background.completeRestorationProcessFirst")
		return
	}
	if r.Method == http.MethodPost {
		var cmd struct{ Action, Session, ID, Kind string }
		if err := decodeAnalysis(r, &cmd); err != nil {
			writeMessage(w, 400, "service.background.invalidBackendTaskParameters")
			return
		}
		if err := a.manageBackground(cmd.Action, cmd.Session, cmd.Kind, cmd.ID, r.Header.Get("X-ExcelLens-Session")); err != nil {
			writeFailure(w, 409, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	var items []backgroundItem
	var sessions []map[string]string
	for _, o := range a.backgroundOwners(r.Header.Get("X-ExcelLens-Session")) {
		set := o.session.results()
		items = append(items, set.items(o.id, o.attach, o.idle)...)
		if name := o.session.fileName(); name != "" && o.idle {
			sessions = append(sessions, map[string]string{"session": o.id, "file": name})
		}
	}
	slices.SortFunc(items, func(a, b backgroundItem) int { return b.Created.Compare(a.Created) })
	for i := range items {
		items[i].Stage = responseLocale(w).Stage(items[i].Stage)
	}
	writeJSON(w, map[string]any{"items": items, "sessions": sessions})
}

func (a *App) backgroundOwners(requester string) []backgroundOwner {
	a.sessionsMu.Lock()
	defer a.sessionsMu.Unlock()
	owners := make([]backgroundOwner, 0, len(a.sessions))
	for id, s := range a.sessions {
		owners = append(owners, backgroundOwner{id, s, id == requester || s.lease.Unattended(time.Now()), s.lease.Idle(time.Now())})
	}
	return owners
}

func (a *App) manageBackground(action, sid, kind, id, requester string) error {
	a.sessionsMu.Lock()
	s := a.sessions[sid]
	if s == nil {
		a.sessionsMu.Unlock()
		return i18n.Error("service.background.taskSessionReleased")
	}
	if action == "release" {
		running, _ := s.work.State(time.Now())
		if !s.lease.Idle(time.Now()) || running {
			a.sessionsMu.Unlock()
			return i18n.Error("service.background.closeTabFirstCancelOr")
		}
		delete(a.sessions, sid)
		a.sessionsMu.Unlock()
		s.close()
		return nil
	}
	protect := !s.lease.Idle(time.Now())
	a.sessionsMu.Unlock()
	if err := s.manageResult(action, kind, id, protect, requester != sid); err != nil {
		return err
	}
	if action == "discard" {
		s.work.Forget(id)
		a.parkUnattended(true, sid)
	}
	return nil
}

// Only live pages and unfinished workers can write workbook data. Sealed
// results have their own source pins and remain available after a restore.
func (a *App) restoreBlockers() error {
	a.sessionGate.Lock()
	defer a.sessionGate.Unlock()
	a.parkUnattended(true, "")
	var names []string
	if name := (&browserSession{app: a}).fileName(); name != "" {
		names = append(names, name)
	}
	kinds := map[string]string{"search": "搜索", "filter": "筛选", "analysis": "分析", "reconcile": "对账", "export": "导出"}
	for _, o := range a.backgroundOwners("") {
		if o.session.saved != nil {
			continue
		}
		name := o.session.fileName()
		for _, j := range o.session.results().items(o.id, o.attach, o.idle) {
			if j.State == "running" || j.State == "queued" {
				names = append(names, j.File+"（"+kinds[j.Kind]+"）")
			}
		}
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	return errors.New("迁入受阻：" + strings.Join(slices.Compact(names), "、") + "。请关闭对应工作簿或在后台任务中取消运行中的任务")
}
