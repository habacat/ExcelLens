package searchservice

import (
	"context"
	"fmt"
	"sync"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type QuickProgress struct {
	Stage            string
	Percent          float64
	SheetsDone, Rows int
	Matches          []workbook.SearchResult
}
type QuickResult struct {
	Matches    []workbook.SearchResult
	Truncated  bool
	SheetsDone int
}

func QuickSheet(wb *workbook.Workbook, idx *workbook.SheetIndex, sheet, query, mode string, ctx context.Context, progress func(int, int, []workbook.SearchResult)) ([]workbook.SearchResult, bool, error) {
	if mode == "advanced" {
		return wb.SearchSheetAdvanced(idx, sheet, query, formats.QuickSearchHits, ctx.Done(), progress)
	}
	return wb.SearchSheet(idx, sheet, query, mode, formats.QuickSearchHits, ctx.Done(), progress)
}

// Two sheet slots overlap index construction and scanning. The consumer releases
// a slot in workbook order; a slow earlier sheet cannot accumulate later pages.
func QuickWorkbook(parent context.Context, wb *workbook.Workbook, query, mode string, admit func(context.Context) (func(), error), progress func(QuickProgress)) (QuickResult, error) {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	type sheetResult struct {
		hits    []workbook.SearchResult
		clipped bool
		rows    int
		err     error
	}
	slots := [2]chan sheetResult{make(chan sheetResult, 1), make(chan sheetResult, 1)}
	scan := func(i int) sheetResult {
		meta := wb.Sheets[i]
		report := func(stage string, ratio float64) {
			progress(QuickProgress{Stage: fmt.Sprintf("%s %d/%d · %s", stage, i+1, len(wb.Sheets), meta.Name), Percent: (float64(i) + ratio) * 100 / float64(len(wb.Sheets))})
		}
		idx := wb.GetCachedIndex(meta.Path)
		if idx == nil {
			report("建立索引", 0)
			release, err := admit(ctx)
			if err != nil {
				return sheetResult{err: err}
			}
			idx = wb.GetCachedIndex(meta.Path)
			if idx == nil {
				idx, err = wb.BuildIndex(meta, func(done, total int64, _ string) {
					if total > 0 {
						report("正在索引", .4*float64(done)/float64(total))
					}
				}, ctx.Done())
			}
			release()
			if err != nil {
				return sheetResult{err: fmt.Errorf("建立 Sheet「%s」索引失败：%w", meta.Name, err)}
			}
		}
		report("搜索", .4)
		last := time.Now()
		hits, truncated, err := QuickSheet(wb, idx, meta.Name, query, mode, ctx, func(scanned, total int, _ []workbook.SearchResult) {
			if time.Since(last) > 250*time.Millisecond && total > 0 {
				report("搜索中", .4+.6*float64(scanned)/float64(total))
				last = time.Now()
			}
		})
		return sheetResult{hits, truncated, idx.RowCount(), err}
	}
	start := func(i int) { workers.Add(1); go func() { defer workers.Done(); slots[i%len(slots)] <- scan(i) }() }
	for i := 0; i < min(len(slots), len(wb.Sheets)); i++ {
		start(i)
	}
	result := QuickResult{Matches: []workbook.SearchResult{}}
	rows := 0
	for i := range wb.Sheets {
		var item sheetResult
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case item = <-slots[i%len(slots)]:
		}
		if item.err != nil {
			return result, item.err
		}
		take := min(len(item.hits), formats.QuickSearchHits-len(result.Matches))
		result.Matches = append(result.Matches, item.hits[:take]...)
		result.SheetsDone++
		rows += item.rows
		progress(QuickProgress{Percent: float64(i+1) * 100 / float64(len(wb.Sheets)), SheetsDone: i + 1, Rows: rows, Matches: result.Matches})
		if len(result.Matches) == formats.QuickSearchHits {
			result.Truncated = item.clipped || take < len(item.hits) || i+1 < len(wb.Sheets)
			break
		}
		if next := i + len(slots); next < len(wb.Sheets) {
			start(next)
		}
	}
	return result, nil
}
