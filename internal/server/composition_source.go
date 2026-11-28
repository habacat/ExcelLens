package server

import (
	"context"
	"encoding/json"

	"iter"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/composition"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/sourceview"
	"excellens/internal/taskmanager"
	"excellens/internal/workbook"
)

var errCompositionSourceReleased = i18n.Error("service.compositionSource.multiTableSourceSnapshotsHave")

type compositionSourceInfo struct {
	Ref    sourceview.TableRef `json:"ref"`
	Name   string              `json:"name"`
	Sheet  string              `json:"sheet"`
	Kind   string              `json:"kind"`
	Format string              `json:"format,omitempty"`
	Rows   int64               `json:"rows"`
	Fields []composition.Field `json:"fields"`
}

type compositionSnapshot struct {
	data  *compositionSnapshotData
	lease *taskmanager.Lease[*compositionSnapshotData]
}

type compositionSnapshotData struct {
	plan    *composition.Append
	info    []compositionSourceInfo
	books   []*workbook.Workbook
	release []func()
	bytes   int64
}

func ownCompositionSnapshot(data *compositionSnapshotData) *compositionSnapshot {
	lease := taskmanager.NewLease(data, func() {
		for i := len(data.release) - 1; i >= 0; i-- {
			data.release[i]()
		}
	})
	return &compositionSnapshot{data: data, lease: lease}
}

func (s *compositionSnapshot) refs() int64 {
	if s == nil || s.lease == nil {
		return 0
	}
	return s.lease.Refs()
}

func (s *compositionSnapshot) retain() (*compositionSnapshot, error) {
	if s == nil || s.lease == nil {
		return nil, errCompositionSourceReleased
	}
	next, err := s.lease.Retain(errCompositionSourceReleased)
	if err != nil {
		return nil, err
	}
	return &compositionSnapshot{data: next.Value(), lease: next}, nil
}

func (s *compositionSnapshot) close() {
	if s == nil || s.lease == nil {
		return
	}
	s.lease.Close()
}

func (s *compositionSnapshot) scan(ctx context.Context, columns []int, visit func(composition.Record) error) error {
	borrowed, err := s.retain()
	if err != nil {
		return err
	}
	defer borrowed.close()
	return borrowed.data.plan.Scan(ctx, columns, visit)
}

// Caller holds a query slot and cacheMu.RLock. All source books are pinned
// before any index or scope work starts.
func (a *App) captureComposition(ctx context.Context, input sourceview.Append) (*compositionSnapshot, error) {
	definition, err := input.Normalized()
	if err != nil {
		return nil, err
	}
	data := &compositionSnapshotData{books: make([]*workbook.Workbook, len(definition.Inputs))}
	owner := ownCompositionSnapshot(data)
	accepted := false
	defer func() {
		if !accepted {
			owner.close()
		}
	}()
	for i, input := range definition.Inputs {
		wb := a.resolveReconcileBook(input.Ref.Book)
		if wb == nil || wb.Fingerprint != input.Ref.Fingerprint {
			return nil, i18n.Error("service.compositionSource.thSourceClosedOrChanged", i18n.Args{"value": i + 1})
		}
		data.books[i] = wb
	}
	data.release = append(data.release, a.pinWorkbooks(data.books...))
	sources := make([]composition.Source, len(data.books))
	var retainedBytes int64
	for i, wb := range data.books {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		source, info, release, weight, err := a.compositionSource(ctx, wb, definition.Inputs[i].Ref)
		if err != nil {
			return nil, err
		}
		if release != nil {
			data.release = append(data.release, release)
		}
		if weight > formats.CompositionSourceBytes-retainedBytes {
			return nil, i18n.Error("service.compositionSource.multiTableSourceSnapshotExceeds")
		}
		retainedBytes += weight
		sources[i] = source
		data.info = append(data.info, info)
	}
	data.plan, err = composition.BindAppend(definition, sources)
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		if err := source.Check(ctx); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	data.bytes = retainedBytes + int64(len(encoded))*4 + int64(len(definition.Fields)*len(definition.Inputs))*128 + 4096
	releaseBudget, err := a.reserveCompositionBytes(data.bytes)
	if err != nil {
		return nil, err
	}
	data.release = append(data.release, releaseBudget)
	accepted = true
	return owner, nil
}

// Source data always comes through the existing adapters. Reconciliation's
// header/stamp model is shared; its reduced Value representation is not used
// for composition, because it cannot carry complete formula/cache state.
func (a *App) compositionSource(ctx context.Context, wb *workbook.Workbook, ref sourceview.TableRef) (composition.Source, compositionSourceInfo, func(), int64, error) {
	if wb == nil || sourceBookID(wb) != ref.Book || wb.Fingerprint != ref.Fingerprint {
		return composition.Source{}, compositionSourceInfo{}, nil, 0, i18n.Error("service.compositionSource.multiTableSourcesHaveInconsistent")
	}
	metadata, err := a.buildReconcileSourceWithHeaderBudget(ctx, wb, reconcileSourceRef{Book: ref.Book, Source: ref.Source, HeaderRow: ref.HeaderRow, Stamp: ref.Stamp}, nil, formats.CompositionSourceRowBytes)
	if err != nil {
		return composition.Source{}, compositionSourceInfo{}, nil, 0, err
	}
	if ref.Stamp == "" {
		ref.Stamp = metadata.Info.Stamp
	}
	ref, err = ref.Normalized()
	if err != nil {
		return composition.Source{}, compositionSourceInfo{}, nil, 0, err
	}
	info := compositionSourceInfo{Ref: ref, Name: metadata.Info.Name, Sheet: metadata.Info.Sheet, Kind: metadata.Info.Kind, Format: metadata.Info.Format, Rows: int64(metadata.Info.Total)}
	var weight int64
	headers := sourceHeaderIdentities(metadata.Info)
	for i, field := range metadata.Info.Fields {
		info.Fields = append(info.Fields, composition.Field{Column: field.Column, Name: field.Name, HeaderHash: field.HeaderHash, HeaderLoaded: field.HeaderLoaded && field.RawName != "", HeaderUnique: headers[i].Unique, Truncated: field.Truncated})
		weight += int64(160 + len(field.Name) + len(field.HeaderHash))
	}
	if strings.HasPrefix(ref.Source, "scratch:") {
		source, extra, err := a.compositionScratch(ctx, wb, ref, info.Fields)
		return source, info, nil, weight + extra, err
	}
	meta, found := wb.FindSheet(strings.TrimPrefix(ref.Source, "wb:"))
	if !found {
		return composition.Source{}, info, nil, 0, i18n.Error("service.compositionSource.multiTableSourceWorksheetsDo")
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return composition.Source{}, info, nil, 0, i18n.Error("service.compositionSource.multiTableSourceIndexesNot")
	}
	maxRow, maxCol := idx.MaxRow, max(1, idx.MaxCol)
	rows := func() iter.Seq[workbook.SourceRowReference] {
		return func(yield func(workbook.SourceRowReference) bool) {
			for pos := idx.FindRowOffset(ref.HeaderRow + 1); pos < idx.RowCount(); pos++ {
				if !yield(workbook.SourceRowReference{Number: idx.RowAt(pos).Number, Position: pos}) {
					return
				}
			}
		}
	}
	var release func()
	if ref.Scope != nil {
		maxRow = max(maxRow, ref.Scope.FrozenThrough)
		captured, err := a.restoreAnalysisSource(ctx, wb, ref.Source, *ref.Scope)
		if err != nil {
			return composition.Source{}, info, nil, 0, err
		}
		release = captured.close
		rows = func() iter.Seq[workbook.SourceRowReference] { return captured.rowsAfter(ref.HeaderRow) }
		total, err := captured.countAfter(ctx, ref.HeaderRow)
		if err != nil {
			release()
			return composition.Source{}, info, nil, 0, err
		}
		info.Rows = int64(total)
		if selection := ref.Scope.Selection; selection != nil {
			maxRow = max(maxRow, selection.StartRow, selection.EndRow)
			maxCol = max(maxCol, selection.EndColumn)
			for column := len(info.Fields) + 1; column <= selection.EndColumn; column++ {
				info.Fields = append(info.Fields, composition.Field{Column: column, Name: workbook.ColumnName(column) + " 列"})
				weight += 192
			}
			info.Fields = slices.DeleteFunc(info.Fields, func(f composition.Field) bool {
				return f.Column < selection.StartColumn || f.Column > selection.EndColumn
			})
		}
	}
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if wb.GetCachedIndex(meta.Path) != idx || sourceBookID(wb) != ref.Book {
			return i18n.Error("service.compositionSource.multiTableSourceIndexOr")
		}
		return wb.CheckSourceUnchanged()
	}
	scan := func(ctx context.Context, columns []int, visit func(composition.SourceRow) error) error {
		if len(columns) == 0 {
			for row := range rows() {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := visit(composition.SourceRow{Position: row.Position, Number: row.Number}); err != nil {
					return err
				}
			}
			return ctx.Err()
		}
		var reference workbook.SourceRowReference
		var referenceError error
		checked := func(yield func(workbook.SourceRowReference) bool) {
			for row := range rows() {
				if err := ctx.Err(); err != nil {
					referenceError = err
					return
				}
				if row.Position >= 0 {
					length, err := sourceRowByteLength(idx, row.Position)
					if err != nil || idx.RowAt(row.Position).Number != row.Number {
						referenceError = i18n.Error("service.compositionSource.multiSheetSourceRowIndex")
						return
					}
					if length > formats.CompositionSourceRowBytes {
						referenceError = i18n.Error("service.compositionSource.singleRowMultiTableSource")
						return
					}
				}
				reference = row
				if !yield(row) {
					return
				}
			}
		}
		err := wb.VisitRowsByReferencesWithOptionsContext(ctx, idx, iter.Seq[workbook.SourceRowReference](checked), columns[0], columns[len(columns)-1], workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, Columns: columns}, func(row workbook.RowData) error {
			return visit(composition.SourceRow{Position: reference.Position, Number: row.Number, Cells: row.Cells})
		})
		if referenceError != nil {
			return referenceError
		}
		return err
	}
	return composition.Source{Ref: ref, Name: info.Name, Sheet: info.Sheet, Format: wb.Format, Fields: info.Fields, Rows: info.Rows, MaxRow: maxRow, MaxCol: maxCol, Check: check, Scan: scan}, info, release, weight, nil
}

func (a *App) compositionScratch(ctx context.Context, wb *workbook.Workbook, ref sourceview.TableRef, fields []composition.Field) (composition.Source, int64, error) {
	workspace, err := a.loadAnalysis(wb)
	if err != nil {
		return composition.Source{}, 0, err
	}
	sheet := workspace.Sheet(strings.TrimPrefix(ref.Source, "scratch:"))
	if sheet == nil || analysisSheetStamp(sheet) != ref.Stamp {
		return composition.Source{}, 0, i18n.Error("service.compositionSource.contentAnalysisTableChangedReselect")
	}
	// loadAnalysis decoded a private immutable snapshot. No editing API receives
	// this object; later saves do not mutate this task's captured strings.
	weight := int64(len(sheet.Rows))*32 + int64(len(sheet.Columns))*32
	for _, row := range sheet.Rows {
		weight += int64(len(row)) * 16
		for _, text := range row {
			weight += int64(len(text))*5/4 + 16
		}
		if weight > formats.CompositionSourceBytes {
			return composition.Source{}, 0, i18n.Error("service.compositionSource.snapshotMultiTableAnalysisTable")
		}
	}
	scan := func(ctx context.Context, columns []int, visit func(composition.SourceRow) error) error {
		for position, row := range sheet.Rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			cells := make([]workbook.CellData, 0, len(columns))
			for _, column := range columns {
				if column < 1 || column > len(sheet.Columns) {
					return i18n.Error("service.compositionSource.fieldsMultiTableAnalysisInvalid")
				}
				if column <= len(row) {
					cells = append(cells, workbook.CellData{Ref: workbook.ColumnName(column) + strconv.Itoa(position+1), Col: column, Raw: row[column-1], Display: row[column-1], Type: "s"})
				}
			}
			if err := visit(composition.SourceRow{Position: position, Number: position + 1, Cells: cells}); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	return composition.Source{Ref: ref, Name: workbookDisplayName(wb), Sheet: sheet.Name, Fields: fields, Rows: int64(len(sheet.Rows)), MaxRow: len(sheet.Rows), MaxCol: len(sheet.Columns), Check: func(ctx context.Context) error { return ctx.Err() }, Scan: scan}, weight, ctx.Err()
}
