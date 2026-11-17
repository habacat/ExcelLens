package server

import (
	"context"
	"errors"
	"sort"
	"time"

	"excellens/internal/workbook"
)

func failFilterJob(job *FilterJob, err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.ctx.Err() != nil || errors.Is(err, context.Canceled) || err.Error() == "filter cancelled" {
		job.Status, job.Stage = "cancelled", "已取消"
	} else {
		job.Status, job.Stage, job.Error = "error", "筛选失败", err.Error()
	}
}

// Only complete candidates enter the public grid. The shared RowOrder keeps
// independent consumers alive after clearing, reopening or pruning this owner.
func (a *App) publishFilterResult(wb *workbook.Workbook, meta workbook.SheetMeta, idx *workbook.SheetIndex, rules []workbook.FilterRule, opts workbook.FilterOptions, matches *workbook.RowOrder, job *FilterJob) {
	result := &FilterResult{ID: job.ID, Sheet: meta.Name, SheetPath: meta.Path, Rows: matches, Sorted: opts.SortColumn > 0 || len(opts.SortKeys) > 0, Created: time.Now(), Spec: newFilterSpec(rules, opts)}
	result.PinnedPositions = make([]int, 0)
	for row := 1; row <= 30; row++ {
		if at, found := result.position(idx, row); found {
			result.PinnedPositions = append(result.PinnedPositions, at)
		}
	}
	sort.Ints(result.PinnedPositions)
	a.mu.Lock()
	published := a.wb == wb && job.ctx.Err() == nil && !a.maintenance
	if published {
		a.filterResults[result.ID] = result
		pruneFilterResultsLocked(a.filterResults, 4)
	}
	a.mu.Unlock()
	if !published {
		result.close()
		failFilterJob(job, context.Canceled)
		return
	}
	job.mu.Lock()
	job.Status, job.Stage, job.Progress = "done", "筛选完成", 100
	job.Scanned, job.ResultID = job.Total, result.ID
	job.mu.Unlock()
}
