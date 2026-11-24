package server

import (
	"excellens/internal/workbook"
	"maps"
	"net/http"
	"slices"
	"time"
)

// Keep only the originating session. Task registries, source pins and existing
// result/download handlers continue to own all data and cancellation.
func (a *App) retainSubmitted(done <-chan struct{}) {
	if a.owner == nil {
		return
	}
	finish := a.submitted.Begin()
	go func() { <-done; finish() }()
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
}

func (a *App) submittedItems(sid string, canAttach bool) []backgroundItem {
	items := []backgroundItem{}
	add := func(id, kind, state, stage, msg string, t time.Time, wb *workbook.Workbook) {
		if wb != nil {
			items = append(items, backgroundItem{id, kind, workbookDisplayName(wb), state, stage, msg, t, sid, canAttach})
		}
	}
	a.mu.RLock()
	exports, searches, filters, wb := maps.Clone(a.exports), maps.Clone(a.searchJobs), maps.Clone(a.filterJobs), a.wb
	a.mu.RUnlock()
	for _, j := range exports {
		j.mu.RLock()
		add(j.ID, "export", j.Status, j.Stage, j.Error, j.Started, j.wb)
		j.mu.RUnlock()
	}
	for _, j := range searches {
		j.mu.RLock()
		add(j.ID, "search", j.Status, j.Stage, j.Error, j.Started, wb)
		j.mu.RUnlock()
	}
	for _, j := range filters {
		j.mu.RLock()
		add(j.ID, "filter", j.Status, j.Stage, j.Error, j.Started, wb)
		j.mu.RUnlock()
	}
	a.analysisMu.Lock()
	analyses := maps.Clone(a.analysisJobs)
	a.analysisMu.Unlock()
	for _, j := range analyses {
		j.mu.RLock()
		add(j.ID, "analysis", j.Status, j.Stage, j.Error, j.Created, j.Workbook)
		j.mu.RUnlock()
	}
	a.reconcileMu.Lock()
	comparisons := maps.Clone(a.reconcileJobs)
	a.reconcileMu.Unlock()
	for _, j := range comparisons {
		j.mu.RLock()
		add(j.ID, "reconcile", j.Status, j.Stage, j.Error, j.Created, j.Workbook)
		j.mu.RUnlock()
	}
	return items
}
func (a *App) handleBackground(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	type owner struct {
		id     string
		s      *browserSession
		attach bool
	}
	var owners []owner
	a.sessionsMu.Lock()
	for id, s := range a.sessions {
		owners = append(owners, owner{id, s, id == r.Header.Get("X-ExcelLens-Session") || s.lease.Unattended(time.Now())})
	}
	a.sessionsMu.Unlock()
	var items []backgroundItem
	for _, o := range owners {
		items = append(items, o.s.app.submittedItems(o.id, o.attach)...)
	}
	slices.SortFunc(items, func(a, b backgroundItem) int { return b.Created.Compare(a.Created) })
	writeJSON(w, map[string]any{"items": items})
}
