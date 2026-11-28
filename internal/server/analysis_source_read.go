package server

import (
	"context"

	"iter"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

func scanWorkbookAnalysisRows(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, rows iter.Seq[workbook.SourceRowReference], need []string, observer sourceCellObserver, visit func(analysis.Record) error) error {
	columns := make([]int, 0, len(need))
	seen := make(map[int]bool, len(need))
	for _, id := range need {
		if !strings.HasPrefix(id, "C") {
			continue
		}
		column, err := strconv.Atoi(strings.TrimPrefix(id, "C"))
		if err == nil && column >= 1 && column <= formats.ExcelColumns && !seen[column] {
			columns = append(columns, column)
			seen[column] = true
		}
	}
	if len(columns) == 0 {
		columns = []int{1}
	}
	if len(columns) > analysis.MaxSourceFields {
		return i18n.Error("service.analysisSourceRead.maximumOriginalFieldsCanBe")
	}
	sort.Ints(columns)
	ids := make(map[int]string, len(columns))
	for _, column := range columns {
		ids[column] = "C" + strconv.Itoa(column)
	}
	capability, _ := formats.Lookup(wb.Format)
	err := wb.VisitRowsByReferencesWithOptionsContext(ctx, idx, rows, columns[0], columns[len(columns)-1], workbook.RowReadOptions{FormatValue: true, Columns: columns}, func(row workbook.RowData) error {
		values := make(map[string]analysis.Value, len(row.Cells))
		for _, cell := range row.Cells {
			numeric := sourceCellNumeric(wb, cell)
			if observer != nil {
				if err := observer(row.Number, cell, numeric); err != nil {
					return err
				}
			}
			text := cell.Raw
			if !numeric {
				text = cell.Display
			}
			textual := capability.TextClustering && !numeric && (cell.Type == "s" || cell.Type == "str" || cell.Type == "inlineStr")
			values[ids[cell.Col]] = analysis.Value{Text: text, Display: cell.Display, Numeric: numeric, MissingCache: cell.CacheMissing, Textual: textual}
		}
		return visit(analysis.Record{Number: row.Number, Values: values})
	})
	if err == nil {
		err = ctx.Err()
	}
	return err
}

// A supplied runtime snapshot is an optimization over the durable scope, not
// an alternative source identity. A mismatch is rejected rather than widened.
// Caller holds cacheMu.RLock for the lifetime of the returned source. Rebuilding
// a saved filter without a borrowed snapshot also requires a query slot.
func (a *App) sourceQuery(ctx context.Context, wb *workbook.Workbook, q analysis.Query, observer sourceCellObserver, borrowed *analysisSourceSnapshot) (analysisSource, error) {
	if err := analysis.ValidateSource(q); err != nil {
		return analysisSource{}, err
	}
	if q.Composition != nil {
		return a.compositionAnalysisSource(ctx, wb, q, observer, borrowed)
	}
	if q.SourceScope == nil {
		if borrowed != nil {
			return analysisSource{}, i18n.Error("service.analysisSourceRead.analysisSourceSnapshotLacksRange")
		}
		return a.sourceObserved(wb, q.Source, q.HeaderRow, observer)
	}
	scope, err := normalizedAnalysisScope(wb, q.Source, *q.SourceScope)
	if err != nil {
		return analysisSource{}, err
	}
	var snapshot *analysisSourceSnapshot
	if borrowed != nil {
		if borrowed.isClosed() || borrowed.data.wb != wb || borrowed.data.source != q.Source || borrowed.data.scope.Digest() != scope.Digest() {
			return analysisSource{}, i18n.Error("service.analysisSourceRead.analysisSourceSnapshotDoesNot")
		}
		meta, found := wb.FindSheet(strings.TrimPrefix(q.Source, "wb:"))
		if !found || wb.GetCachedIndex(meta.Path) != borrowed.data.index {
			return analysisSource{}, i18n.Error("service.analysisSourceRead.analysisSourceIndexChanged")
		}
		snapshot, err = borrowed.retain()
	} else {
		snapshot, err = a.restoreAnalysisSource(ctx, wb, q.Source, scope)
	}
	if err != nil {
		return analysisSource{}, err
	}
	accepted := false
	defer func() {
		if !accepted {
			snapshot.close()
		}
	}()
	base, err := a.sourceObserved(wb, q.Source, q.HeaderRow, nil)
	if err != nil {
		return analysisSource{}, err
	}
	if selection := scope.Selection; selection != nil {
		names := make(map[string]bool, len(base.fields))
		for _, field := range base.fields {
			names[field.Name] = true
		}
		for column := len(base.fields) + 1; column <= selection.EndColumn; column++ {
			base.fields = append(base.fields, workbookAnalysisField(column, "", names))
		}
		base.fields = base.fields[selection.StartColumn-1 : selection.EndColumn]
		base.truncatedFields = false
	}
	total, err := snapshot.countAfter(ctx, q.HeaderRow)
	if err != nil {
		return analysisSource{}, err
	}
	base.total = total
	base.stamp = func() string { return snapshot.data.scope.Digest() }
	base.lease = snapshot
	base.scan = func(ctx context.Context, need []string, visit func(analysis.Record) error) error {
		if selection := scope.Selection; selection != nil {
			if len(need) == 0 {
				need = []string{"C" + strconv.Itoa(selection.StartColumn)}
			}
			for _, id := range need {
				column, _ := strconv.Atoi(strings.TrimPrefix(id, "C"))
				if column > 0 && (column < selection.StartColumn || column > selection.EndColumn) {
					return i18n.Error("service.analysisSourceRead.analyzedFieldsNotWithinCaptured")
				}
			}
		}
		return scanWorkbookAnalysisRows(ctx, wb, snapshot.data.index, snapshot.rowsAfter(q.HeaderRow), need, observer, visit)
	}
	accepted = true
	return base, nil
}

func (s analysisSource) close() { s.lease.close() }

func (job *analysisTask) closeSourceRows() {
	job.mu.Lock()
	source := job.SourceRows
	job.SourceRows = nil
	job.mu.Unlock()
	source.close()
}

// Callers keep the cache read lock and close the returned source after reading.
func (a *App) sourceRequest(ctx context.Context, wb *workbook.Workbook, source string, header int, id string) (analysisSource, error) {
	return a.sourceRequestObserved(ctx, wb, source, header, id, nil)
}

func (a *App) sourceRequestObserved(ctx context.Context, wb *workbook.Workbook, source string, header int, id string, observer sourceCellObserver) (analysisSource, error) {
	q := analysis.Query{Source: source, HeaderRow: header}
	var captured *analysisSourceSnapshot
	if strings.HasPrefix(source, "append:") {
		owner, err := a.borrowCompositionAnalysisSource(wb, source, id)
		if err != nil {
			return analysisSource{}, err
		}
		defer owner.close()
		q.HeaderRow = 0
		q, err = compositionAnalysisDefinition(q, owner)
		if err != nil {
			return analysisSource{}, err
		}
		return a.sourceQuery(ctx, wb, q, observer, owner)
	}
	if id != "" {
		var err error
		captured, err = a.analysisSourceSnapshot(id, wb)
		if err != nil {
			return analysisSource{}, err
		}
		defer captured.close()
		q.SourceScope = captured.canonicalScope()
	}
	return a.sourceQuery(ctx, wb, q, observer, captured)
}

func (a *App) sourceResult(ctx context.Context, wb *workbook.Workbook, q analysis.Query, id string) (analysisSource, error) {
	if id == "" {
		if q.Composition != nil {
			return analysisSource{}, i18n.Error("service.analysisSourceRead.openCombinedSourceDetailsFrom")
		}
		return a.sourceQuery(ctx, wb, q, nil, nil)
	}
	job := a.getAnalysisJob(id)
	if job == nil {
		return analysisSource{}, i18n.Error("service.analysisSourceRead.analysisResultsHaveExpiredRun")
	}
	job.mu.RLock()
	if job.Workbook != wb || job.Status != "done" || job.Query.Source != q.Source || job.Query.HeaderRow != q.HeaderRow || scopeDigest(job.Query) != scopeDigest(q) {
		job.mu.RUnlock()
		return analysisSource{}, i18n.Error("service.analysisSourceRead.analysisResultDoesNotMatch")
	}
	var captured *analysisSourceSnapshot
	var err error
	if job.SourceRows != nil {
		captured, err = job.SourceRows.retain()
	} else if q.SourceScope != nil || q.Composition != nil {
		err = i18n.Error("service.analysisSourceRead.sourceSnapshotAnalysisResultsReleased")
	}
	job.mu.RUnlock()
	if err != nil {
		return analysisSource{}, err
	}
	defer captured.close()
	return a.sourceQuery(ctx, wb, q, nil, captured)
}

func scopeDigest(q analysis.Query) string {
	if q.SourceScope == nil {
		return ""
	}
	return q.SourceScope.Digest()
}

func normalizedAnalysisScope(wb *workbook.Workbook, source string, input sourceview.Scope) (sourceview.Scope, error) {
	if wb == nil || !strings.HasPrefix(source, "wb:") || input.Fingerprint != wb.Fingerprint {
		return sourceview.Scope{}, i18n.Error("service.analysisSourceRead.analysisSourceRangeDoesNot")
	}
	capability, ok := formats.Lookup(wb.Format)
	if !ok {
		return sourceview.Scope{}, i18n.Error("service.analysisSourceRead.analysisSourceFormatInvalid")
	}
	scope, err := input.Normalized(capability.MaxRows, capability.MaxColumns)
	if err != nil {
		return sourceview.Scope{}, err
	}
	if scope.Selection != nil && scope.Selection.EndColumn > analysis.MaxSourceColumns {
		return sourceview.Scope{}, i18n.Error("service.analysisSourceRead.analysisSelectionExceedsFirstSource")
	}
	return scope, nil
}

func scopedAnalysisPreview(ctx context.Context, wb *workbook.Workbook, src *analysisSource, header int) ([]analysis.DrillRow, error) {
	columns := make([]int, len(src.fields))
	slots := make(map[int]int, len(columns))
	for i, f := range src.fields {
		columns[i] = f.Column
		slots[f.Column] = i
	}
	counts, numbers := make([]int, len(columns)), make([]int, len(columns))
	sampled := make([]analysis.DrillRow, 0, 8)
	rows := func(yield func(workbook.SourceRowReference) bool) {
		seen := 0
		for ref := range src.lease.rowsAfter(header) {
			if !yield(ref) {
				return
			}
			seen++
			if seen == 8 {
				return
			}
		}
	}
	err := wb.VisitRowsByReferencesWithOptionsContext(ctx, src.lease.data.index, rows, columns[0], columns[len(columns)-1], workbook.RowReadOptions{FormatValue: true, Columns: columns}, func(row workbook.RowData) error {
		cells := make([]string, min(16, len(columns)))
		for _, cell := range row.Cells {
			slot, found := slots[cell.Col]
			if !found {
				continue
			}
			text := cell.Raw
			if cell.Type != "" && cell.Type != "n" {
				text = cell.Display
			}
			if strings.TrimSpace(text) != "" {
				counts[slot]++
				if sourceCellNumeric(wb, cell) {
					numbers[slot]++
				}
			}
			if slot < len(cells) {
				cells[slot] = cell.Display
			}
		}
		sampled = append(sampled, analysis.DrillRow{Row: row.Number, Cells: cells})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range src.fields {
		src.fields[i].Numeric = counts[i] > 0 && numbers[i]*10 >= counts[i]*7
	}
	return sampled, nil
}
