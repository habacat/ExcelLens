package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"

	"fmt"
	"iter"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/archives"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

const maxReconcileAttachments = formats.SourceAttachmentFiles
const maxReconcileProjectionBytes = 16 << 20
const maxSourceHeaderColumns = 512

type reconcileAttachment struct {
	book    *workbook.Workbook
	release func()
}

type reconcileSourceRef struct {
	Book      string `json:"book"`
	Source    string `json:"source"`
	HeaderRow int    `json:"headerRow"`
	Stamp     string `json:"stamp,omitempty"`
}

type reconcileBookChoice struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	Attached      bool                   `json:"attached"`
	Incomplete    bool                   `json:"incomplete"`
	Sheets        []workbook.SheetMeta   `json:"sheets"`
	Scratch       []reconcileSheetChoice `json:"scratch"`
	ScratchLoaded bool                   `json:"scratchLoaded"`
}

type reconcileSheetChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *App) handleReconcileSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if requireAnalysisWorkbook(a, w) == nil {
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	books := a.referencedWorkbooks()
	sort.Slice(books, func(i, j int) bool { return sourceBookID(books[i]) < sourceBookID(books[j]) })
	seen := make(map[string]bool)
	focus := r.URL.Query().Get("book")
	if focus == "" {
		focus = sourceBookID(a.currentWorkbook())
	}
	choices := make([]reconcileBookChoice, 0, len(books))
	for _, wb := range books {
		id := sourceBookID(wb)
		if seen[id] {
			continue
		}
		seen[id] = true
		a.reconcileMu.Lock()
		attached := a.reconcileBooks[id] != nil
		a.reconcileMu.Unlock()
		choice := reconcileBookChoice{ID: id, Name: workbookDisplayName(wb), Sheets: wb.Sheets, Attached: attached, Incomplete: wb.SourceInfo.Incomplete}
		if id == focus {
			workspace, err := a.loadAnalysis(wb)
			if err != nil {
				writeMessage(w, 409, "service.reconcileSource.failedReadSourceAnalysisWorkspace", i18n.Args{"detail": err})
				return
			}
			for _, sheet := range workspace.Sheets {
				choice.Scratch = append(choice.Scratch, reconcileSheetChoice{ID: sheet.ID, Name: sheet.Name})
			}
			choice.ScratchLoaded = true
		}
		choices = append(choices, choice)
	}
	writeJSON(w, map[string]any{"books": choices, "currentBook": sourceBookID(a.currentWorkbook())})
}

// The normal upload handler owns the single extension/size/persistence path.
// A sidecar upload attaches a source, without replacing the current workbook.
func (a *App) handleReconcileUploaded(w http.ResponseWriter, r *http.Request, path, name string) {
	accepted := false
	defer func() {
		if !accepted {
			_ = os.Remove(path)
		}
	}()
	if archives.IsPath(name) {
		writeMessage(w, 400, "service.reconcileSource.compressedPackagesOpenRequiredMembers")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	primary := a.currentWorkbook()
	if primary == nil || primary.Fingerprint != r.FormValue("workbookKey") || sourceBookID(primary) != r.FormValue("primaryBook") {
		writeMessage(w, 409, "service.reconcileSource.currentWorkbookSwitchedReAdd")
		return
	}
	book, err := workbook.OpenWorkbook(path)
	if err != nil {
		writeMessage(w, 400, "service.reconcileSource.unableOpenVerificationSource", i18n.Args{"detail": err})
		return
	}
	book.SetDisplayName(name)
	id := sourceBookID(book)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != primary || a.maintenance {
		writeMessage(w, 409, "service.reconcileSource.workbookSwitchedOrCurrentlyUnder")
		return
	}
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	if len(a.reconcileBooks) >= maxReconcileAttachments {
		writeMessage(w, 429, "service.reconcileSource.upSourceFilesCanBe", i18n.Args{"maxReconcileAttachments": maxReconcileAttachments})
		return
	}
	a.reconcileBooks[id] = &reconcileAttachment{book: book, release: a.pinWorkbooks(book)}
	accepted = true
	writeJSON(w, map[string]any{"book": id, "name": name, "sheets": book.Sheets})
}

func (a *App) handleReconcileRemoveSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Book string `json:"book"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeMessage(w, 400, "service.reconcileSource.invalidSourceParameters")
		return
	}
	a.reconcileMu.Lock()
	attachment := a.reconcileBooks[req.Book]
	delete(a.reconcileBooks, req.Book)
	a.reconcileMu.Unlock()
	if attachment != nil {
		attachment.release()
	}
	writeJSON(w, map[string]bool{"removed": attachment != nil})
}

func sourceField(column int, raw string, loaded bool) reconcile.Field {
	name := strings.TrimSpace(raw)
	text, truncated := boundedSourceLabel(name, 128)
	if text == "" {
		text = workbook.ColumnName(column) + " 列"
	}
	field := reconcile.Field{Column: column, Name: text, HeaderLoaded: loaded, Truncated: truncated}
	if loaded {
		field.RawName, _ = boundedSourceLabel(name, 128)
		hash := sha256.Sum256([]byte(raw))
		field.HeaderHash = fmt.Sprintf("%x", hash[:])
	}
	return field
}

func boundedSourceLabel(text string, limit int) (string, bool) {
	count := 0
	for at := range text {
		if count == limit {
			return strings.Clone(text[:at]), true
		}
		count++
	}
	return strings.Clone(text), false
}

func (a *App) buildReconcileSource(ctx context.Context, wb *workbook.Workbook, ref reconcileSourceRef, columns []int) (reconcile.Source, error) {
	return a.buildReconcileSourceWithHeaderBudget(ctx, wb, ref, columns, 0)
}

func (a *App) buildReconcileSourceWithHeaderBudget(ctx context.Context, wb *workbook.Workbook, ref reconcileSourceRef, columns []int, headerBudget int64) (reconcile.Source, error) {
	return a.buildTypedTableSource(ctx, wb, ref, columns, headerBudget, reconcile.MaxKeys+reconcile.MaxFields)
}

// The adapter and selected snapshot are shared by comparisons and explicit
// relations. Each caller keeps its own operation limit; old comparison limits
// and source interpretation are unchanged.
func (a *App) buildTypedTableSource(ctx context.Context, wb *workbook.Workbook, ref reconcileSourceRef, columns []int, headerBudget int64, columnLimit int) (reconcile.Source, error) {
	if wb == nil || sourceBookID(wb) != ref.Book || len(ref.Source) > 512 {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.sourceVerificationClosedOrIdentifier")
	}
	if wb.SourceInfo.Incomplete || wb.ArchiveOrigin != nil && wb.ArchiveOrigin.Partial {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.thisSourceIncompleteReadStatus")
	}
	for _, column := range columns {
		if column < 1 || column > formats.ExcelColumns {
			return reconcile.Source{}, i18n.Error("service.reconcileSource.verificationFieldColumnNumberInvalid")
		}
	}
	if len(columns) > columnLimit {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.excessiveReferencedFieldsDuringVerification")
	}
	if strings.HasPrefix(ref.Source, "scratch:") {
		return a.reconcileScratchSource(wb, ref, columns)
	}
	if err := wb.CheckSourceUnchanged(); err != nil {
		return reconcile.Source{}, err
	}
	if !strings.HasPrefix(ref.Source, "wb:") {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.invalidSourceTypeVerification")
	}
	meta, found := wb.FindSheet(strings.TrimPrefix(ref.Source, "wb:"))
	if !found {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.verificationWorksheetDoesNotExist")
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		releaseIndex, admissionErr := a.backgroundIndex(ctx)
		if admissionErr != nil {
			return reconcile.Source{}, ctx.Err()
		}
		var err error
		idx, err = wb.BuildIndex(meta, nil, ctx.Done())
		releaseIndex()
		if err != nil {
			return reconcile.Source{}, err
		}
	}
	if ref.HeaderRow < 0 || ref.HeaderRow > idx.MaxRow {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.rowNumberVerificationHeaderExceeds")
	}
	if headerBudget > 0 && ref.HeaderRow > 0 {
		if err := sourceRowsWithinBudget(idx, []int{ref.HeaderRow}, headerBudget); err != nil {
			return reconcile.Source{}, err
		}
	}
	first := idx.FindRowOffset(ref.HeaderRow + 1)
	info := reconcile.SourceInfo{ID: ref.Book + "/" + ref.Source, Name: workbookDisplayName(wb), Sheet: meta.Name, Kind: "workbook", Format: wb.Format, Fingerprint: wb.Fingerprint, HeaderRow: ref.HeaderRow, Total: idx.RowCount() - first}
	if wb.Format == "sqlite" {
		info.Notice = "SQLite缓存未保留原始类型；请显式选择按文本或数值核对，NULL/BLOB占位不参与配对。"
	}
	var headerCells []workbook.CellData
	if ref.HeaderRow > 0 {
		at := idx.FindRowOffset(ref.HeaderRow)
		if at < idx.RowCount() && idx.RowAt(at).Number == ref.HeaderRow {
			row, err := wb.ReadRowsContext(ctx, idx, at, 1, 1, min(idx.MaxCol, maxSourceHeaderColumns))
			if err != nil {
				return reconcile.Source{}, err
			}
			if len(row) > 0 {
				headerCells = row[0].Cells
			}
		}
	}
	labels := make(map[int]workbook.CellData, len(headerCells))
	for _, cell := range headerCells {
		labels[cell.Col] = cell
	}
	for col := 1; col <= max(1, idx.MaxCol); col++ {
		cell, exists := labels[col]
		info.Fields = append(info.Fields, sourceField(col, cell.Display, exists && !cell.CacheMissing && cell.Type != "e"))
	}
	stamp, _ := json.Marshal([]any{1, wb.Fingerprint, meta.Path, ref.HeaderRow})
	hash := sha256.Sum256(stamp)
	info.Stamp = fmt.Sprintf("%x", hash[:])
	if ref.Stamp != "" && ref.Stamp != info.Stamp {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.sourceOrHeaderChangedReload")
	}
	read := func(ctx context.Context, positions iter.Seq[int], cols []int, visit func(reconcile.Record) error) error {
		if len(cols) == 0 {
			return i18n.Error("service.reconcileSource.noVerificationFieldsSelected")
		}
		// Native visitor preserves source coordinates; the position iterator's
		// current entry is captured before each synchronous row callback.
		current := -1
		tracked := func(yield func(int) bool) {
			for pos := range positions {
				current = pos
				if !yield(pos) {
					return
				}
			}
		}
		return wb.VisitRowsWithOptionsContext(ctx, idx, iter.Seq[int](tracked), cols[0], cols[len(cols)-1], workbook.RowReadOptions{FormatValue: true, Columns: cols}, func(row workbook.RowData) error {
			values := make(map[int]reconcile.Value, len(row.Cells))
			for _, cell := range row.Cells {
				value := reconcile.Value{Raw: cell.Raw, Display: cell.Display, Type: cell.Type, Exists: true, MissingCache: cell.CacheMissing}
				value.TypeMissing = wb.Format == "sqlite"
				if wb.Format == "sqlite" && ambiguousSQLitePlaceholder(cell.Raw) {
					value.Uncertain = "SQLite缓存中的NULL/BLOB占位无法可靠区分原始类型"
				}
				values[cell.Col] = value
			}
			return visit(reconcile.Record{Position: current, Number: row.Number, Values: values})
		})
	}
	scan := func(ctx context.Context, cols []int, visit func(reconcile.Record) error) error {
		positions := func(yield func(int) bool) {
			for pos := first; pos < idx.RowCount(); pos++ {
				if !yield(pos) {
					return
				}
			}
		}
		return read(ctx, iter.Seq[int](positions), cols, visit)
	}
	return reconcile.Source{Info: info, SourceRows: idx.RowCount(), Scan: scan, Read: read}, nil
}

func ambiguousSQLitePlaceholder(raw string) bool {
	if raw == "[NULL]" {
		return true
	}
	if !strings.HasPrefix(raw, "[BLOB ") || !strings.HasSuffix(raw, " bytes]") {
		return false
	}
	count := strings.TrimSuffix(strings.TrimPrefix(raw, "[BLOB "), " bytes]")
	_, err := strconv.ParseUint(count, 10, 64)
	return err == nil
}

func (a *App) reconcileScratchSource(wb *workbook.Workbook, ref reconcileSourceRef, columns []int) (reconcile.Source, error) {
	workspace, err := a.loadAnalysis(wb)
	if err != nil {
		return reconcile.Source{}, err
	}
	sheet := workspace.Sheet(strings.TrimPrefix(ref.Source, "scratch:"))
	if sheet == nil {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.analysisEditTableDoesNot")
	}
	info := reconcile.SourceInfo{ID: ref.Book + "/" + ref.Source, Name: workbookDisplayName(wb), Sheet: sheet.Name, Kind: "analysis", Fingerprint: wb.Fingerprint, Total: len(sheet.Rows)}
	info.Stamp = analysisSheetStamp(sheet)
	if ref.Stamp != "" && ref.Stamp != info.Stamp {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.analysisEditingTableModifiedReload")
	}
	for col, name := range sheet.Columns {
		info.Fields = append(info.Fields, sourceField(col+1, name, true))
	}
	source := reconcile.Source{Info: info, SourceRows: len(sheet.Rows)}
	if columns == nil {
		return source, nil
	} // metadata requests do not retain rows
	bytes := int64(len(sheet.Rows)) * int64(32+len(columns)*96)
	if bytes > maxReconcileProjectionBytes {
		return reconcile.Source{}, i18n.Error("service.reconcileSource.analysisEditingTableSnapshotThat")
	}
	projection := make([][]reconcile.Value, len(sheet.Rows))
	for pos, row := range sheet.Rows {
		values := make([]reconcile.Value, len(columns))
		for slot, col := range columns {
			if col > len(sheet.Columns) {
				return reconcile.Source{}, i18n.Error("service.reconcileSource.analysisEditTableFieldsHave")
			}
			if col <= len(row) {
				text := row[col-1]
				bytes += int64(len(text))*5/4 + 16
				if bytes > maxReconcileProjectionBytes {
					return reconcile.Source{}, i18n.Error("service.reconcileSource.analysisEditTableSnapshotExceeds")
				}
				text = strings.Clone(text)
				values[slot] = reconcile.Value{Raw: text, Display: text, Type: "s", Exists: true}
			}
		}
		projection[pos] = values
	}
	source.RetainedBytes = bytes
	source.Read = func(ctx context.Context, positions iter.Seq[int], _ []int, visit func(reconcile.Record) error) error {
		for pos := range positions {
			if err := ctx.Err(); err != nil {
				return err
			}
			if pos < 0 || pos >= len(projection) {
				return i18n.Error("service.reconcileSource.editingTableAnalysisNotCorrect")
			}
			values := make(map[int]reconcile.Value, len(columns))
			for slot, col := range columns {
				values[col] = projection[pos][slot]
			}
			if err := visit(reconcile.Record{Position: pos, Number: pos + 1, Values: values}); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	read := source.Read
	source.Scan = func(ctx context.Context, cols []int, visit func(reconcile.Record) error) error {
		positions := func(yield func(int) bool) {
			for pos := range projection {
				if !yield(pos) {
					return
				}
			}
		}
		return read(ctx, iter.Seq[int](positions), cols, visit)
	}
	return source, nil
}

func (a *App) handleReconcileFields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if requireAnalysisWorkbook(a, w) == nil {
		return
	}
	var ref reconcileSourceRef
	if decodeAnalysis(r, &ref) != nil {
		writeMessage(w, 400, "service.reconcileSource.verificationSourceParametersInvalid")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	wb := a.resolveReconcileBook(ref.Book)
	source, err := a.buildReconcileSource(r.Context(), wb, ref, nil)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	markReconcileHeaders(source.Info)
	writeJSON(w, source.Info)
}
