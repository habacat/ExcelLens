package server

import (
	"context"
	"errors"
	"maps"
	"time"

	"excellens/internal/workbook"
)

// An unattended completed result owns just its immutable source and the five
// public job registries. Grid, edit bindings, archives, query caches and other
// page resources retire with the old tab. Viewing a result creates a fresh tab.
type backgroundResults struct {
	book        *workbook.Workbook
	searches    map[string]*SearchJob
	filters     map[string]*FilterJob
	orders      map[string]*FilterResult
	exports     exportController
	analyses    map[string]*analysisTask
	comparisons map[string]*reconcileTask
	release     func()
}

func (a *App) lockResults() func() {
	a.analysisMu.Lock()
	a.mu.Lock()
	a.reconcileMu.Lock()
	return func() { a.reconcileMu.Unlock(); a.mu.Unlock(); a.analysisMu.Unlock() }
}
func (a *App) resultSetLocked() backgroundResults {
	return backgroundResults{book: a.wb, searches: a.searchJobs, filters: a.filterJobs, orders: a.filterResults, exports: a.exports, analyses: a.analysisJobs, comparisons: a.reconcileJobs}
}
func (s *browserSession) results() backgroundResults {
	if s.saved != nil {
		return *s.saved
	}
	a := s.app
	unlock := a.lockResults()
	defer unlock()
	set := a.resultSetLocked()
	set.searches = maps.Clone(set.searches)
	set.filters = maps.Clone(set.filters)
	set.orders = maps.Clone(set.orders)
	set.exports = maps.Clone(set.exports)
	set.analyses = maps.Clone(set.analyses)
	set.comparisons = maps.Clone(set.comparisons)
	return set
}
func (a *App) takeResults() *backgroundResults {
	unlock := a.lockResults()
	defer unlock()
	set := a.resultSetLocked()
	set.release = a.pinWorkbooks(set.book)
	a.searchJobs = nil
	a.filterJobs = nil
	a.filterResults = nil
	a.exports = nil
	a.analysisJobs = nil
	a.reconcileJobs = nil
	return &set
}
func (set *backgroundResults) attach(a *App) {
	a.wb = set.book
	a.searchJobs, a.filterJobs, a.filterResults, a.exports = set.searches, set.filters, set.orders, set.exports
	a.analysisJobs, a.reconcileJobs = set.analyses, set.comparisons
	set.release()
}
func (set *backgroundResults) close() {
	for _, j := range set.searches {
		j.Results.Close()
	}
	for _, j := range set.orders {
		j.close()
	}
	for _, j := range set.analyses {
		j.closeSourceRows()
	}
	for _, j := range set.comparisons {
		j.closeSources()
	}
	set.release()
}
func (s *browserSession) close() {
	if s.saved != nil {
		s.saved.close()
		return
	}
	s.app.Close()
}
func (s *browserSession) fileName() string {
	if s.saved != nil {
		if s.saved.book != nil {
			return workbookDisplayName(s.saved.book)
		}
		return ""
	}
	if wb := s.app.currentWorkbook(); wb != nil {
		return workbookDisplayName(wb)
	}
	s.app.archiveMu.Lock()
	defer s.app.archiveMu.Unlock()
	if view := s.app.archiveView; view != nil {
		return view.file.Name
	}
	if s.app.archiveTask != nil {
		return "正在打开的压缩包"
	}
	return ""
}

// The caller holds sessionGate exclusively. Never hold the registry mutex
// while waiting for another worker to join or when taking workbook locks.
func (a *App) parkUnattended(force bool, only string) {
	now := time.Now()
	owners := a.backgroundOwners("")
	for _, o := range owners {
		s := o.session
		if only != "" && only != o.id {
			continue
		}
		a.sessionsMu.Lock()
		eligible := s.lease.Expired(now) || force && s.lease.Idle(now)
		a.sessionsMu.Unlock()
		if !eligible {
			continue
		}
		running, keep := s.work.State(now)
		if running {
			continue
		}
		if !keep {
			a.sessionsMu.Lock()
			delete(a.sessions, o.id)
			a.sessionsMu.Unlock()
			s.close()
		} else if s.saved == nil {
			old := s.app
			saved := old.takeResults()
			a.sessionsMu.Lock()
			s.saved, s.app, s.routes = saved, nil, nil
			a.sessionsMu.Unlock()
			old.Close()
		}
	}
}

func (s *browserSession) manageResult(action, kind, id string, protect, otherPage bool) error {
	var set backgroundResults
	if s.saved != nil {
		set = *s.saved
	} else {
		unlock := s.app.lockResults()
		defer unlock()
		set = s.app.resultSetLocked()
	}
	var done <-chan struct{}
	var cancel context.CancelFunc
	var remove func()
	switch kind {
	case "search":
		if j := set.searches[id]; j != nil {
			if action == "discard" && protect && otherPage {
				j.mu.RLock()
				inUse := j.Results != nil
				j.mu.RUnlock()
				if inUse {
					return errors.New("活动页面正在使用全量搜索结果，请先关闭该页")
				}
			}
			done, cancel = j.done, j.cancel
			remove = func() {
				delete(set.searches, id)
				j.Results.Close()
			}
		}
	case "filter":
		if j := set.filters[id]; j != nil {
			if action == "discard" && protect {
				j.mu.RLock()
				inUse := set.orders[j.ResultID] != nil
				j.mu.RUnlock()
				if inUse {
					return errors.New("活动页面正在使用此筛选，请先清除筛选或关闭该页")
				}
			}
			done, cancel = j.done, j.cancel
			remove = func() {
				delete(set.filters, id)
				result := set.orders[j.ResultID]
				delete(set.orders, j.ResultID)
				result.close()
			}
		}
	case "export":
		if j := set.exports[id]; j != nil {
			done, cancel = j.done, j.cancel
			remove = func() { delete(set.exports, id) }
		}
	case "analysis":
		if j := set.analyses[id]; j != nil {
			done, cancel = j.done, j.cancel
			remove = func() { delete(set.analyses, id); j.closeSourceRows() }
		}
	case "reconcile":
		if j := set.comparisons[id]; j != nil {
			done, cancel = j.done, j.cancel
			remove = func() { delete(set.comparisons, id); j.closeSources() }
		}
	}
	if done == nil {
		return errors.New("任务已到期或不存在")
	}
	select {
	case <-done:
		if action != "discard" {
			return errors.New("任务已结束，可丢弃结果")
		}
		remove()
	default:
		if action != "cancel" {
			return errors.New("请先取消或等待任务完成")
		}
		cancel()
	}
	return nil
}
