package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"excellens/internal/checkpoint"
	"excellens/internal/i18n"
	"excellens/internal/searchresults"
	"excellens/internal/searchservice"
	"excellens/internal/workbook"
)

func (a *App) runFullSearch(wb *workbook.Workbook, j *SearchJob) {
	results, err := wb.NewSearchResults(j.ctx)
	if err == nil {
		// Transfer ownership on success. An interrupted scan or a recovered
		// worker panic must still return the temporary file and its quota.
		defer func() {
			if results != nil {
				results.Close()
			}
		}()
		err = a.scanFullSearch(wb, j, results)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err != nil {
		if errors.Is(err, context.Canceled) || j.ctx.Err() != nil {
			j.Status, j.Stage = "cancelled", "stage.task.cancelled"
		} else {
			j.Status, j.Error = "error", err.Error()
		}
		return
	}
	j.Results, j.HitCount = results, results.Count()
	results = nil
	j.Status, j.Stage, j.Progress = "done", "stage.task.searchCompleted", 100
	j.SheetsDone = j.SheetsTotal
}
func (a *App) scanFullSearch(wb *workbook.Workbook, j *SearchJob, result *searchresults.Store) error {
	p := searchservice.Plan{Query: j.Query, Mode: j.Mode}
	for _, meta := range wb.Sheets {
		if j.Scope == "all" || meta.Name == j.Sheet {
			p.Sheets = append(p.Sheets, searchservice.Sheet{Name: meta.Name, Path: meta.Path})
		}
	}
	var policy taskIndexes
	if j.checkpoint != nil {
		lease := j.checkpoint.lease
		m := lease.Snapshot()
		spec, err := searchCheckpointSpecFor(m)
		if err != nil || !spec.Full {
			return checkpoint.ErrBinding
		}
		def, err := searchTaskDefinition(wb, searchRequest{Sheet: j.Sheet, Query: j.Query, Mode: j.Mode, Scope: j.Scope, Full: true})
		if err != nil {
			return err
		}
		binding, err := taskBinding(j.ctx, wb, def, m, true)
		if err != nil || binding != m.DefinitionHash {
			return checkpoint.ErrBinding
		}
		policy, err = indexPolicy(wb, m)
		if err != nil {
			return err
		}
		p.Lease = lease
		p.Sheets = spec.Sheets
		p.Expected = policy.expected
		p.Stamp = func(meta workbook.SheetMeta, idx *workbook.SheetIndex) (string, error) {
			return wb.CheckpointIndexStamp(j.ctx, meta, idx, policy.portable)
		}
		p.Advance = func(next checkpoint.Advance, source *io.SectionReader) (checkpoint.Manifest, error) {
			return retryCheckpointBusy(j.ctx, func() (checkpoint.Manifest, error) {
				if _, err := source.Seek(0, io.SeekStart); err != nil {
					return checkpoint.Manifest{}, err
				}
				return lease.Advance(j.ctx, next, source)
			})
		}
	}
	p.Index = func(meta workbook.SheetMeta, stamp string) (*workbook.SheetIndex, error) {
		if idx := wb.GetCachedIndex(meta.Path); idx != nil {
			return idx, nil
		}
		if stamp != "" && !policy.rebuild {
			return nil, i18n.Error("service.searchFull.indexRequiredSearchRecoveryCleared")
		}
		release, err := a.backgroundIndex(j.ctx)
		if err != nil {
			return nil, err
		}
		defer release()
		if idx := wb.GetCachedIndex(meta.Path); idx != nil {
			return idx, nil
		}
		return wb.BuildIndex(meta, nil, j.ctx.Done())
	}
	p.Progress = func(sheet, next, rows, before int, hits int64) {
		j.mu.Lock()
		defer j.mu.Unlock()
		j.HitCount = hits
		j.SheetsDone, j.SheetsTotal = sheet, len(p.Sheets)
		j.Scanned, j.Total = before+next, before+rows
		j.Progress = (float64(sheet) + float64(next)/float64(max(rows, 1))) * 100 / float64(len(p.Sheets))
		j.Stage = "stage.task.fullSearch" + p.Sheets[sheet].Name
	}
	return p.Scan(j.ctx, wb, result)
}
func (a *App) handleSearchResults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	a.mu.RLock()
	j := a.searchJobs[q.Get("id")]
	a.mu.RUnlock()
	if j == nil {
		writeMessage(w, 404, "service.searchFull.searchResultsReleased")
		return
	}
	j.mu.RLock()
	ready := j.Full && j.Status == "done"
	set := j.Results
	ticket := j.Ticket
	j.mu.RUnlock()
	download := r.URL.Path == "/api/search/export"
	if download && (ticket == "" || ticket != q.Get("ticket")) {
		http.Error(w, "Forbidden", 403)
		return
	}
	if !ready || set == nil {
		writeMessage(w, 409, "service.searchFull.fullSearchResultsNotYet")
		return
	}
	filter := searchresults.Filter{Text: q.Get("text"), Sheet: q.Get("sheet")}
	if download {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="ExcelLens-search.csv"`)
		if err := set.CSV(r.Context(), w, filter); err != nil {
			panic(http.ErrAbortHandler)
		}
		return
	}
	var start int64
	if raw := q.Get("after"); raw != "" {
		var err error
		start, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeMessage(w, 400, "service.searchFull.invalidPagingCursor")
			return
		}
	}
	page, err := set.Page(r.Context(), start, filter)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	writeJSON(w, page)
}
