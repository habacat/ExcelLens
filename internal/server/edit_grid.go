package server

import (
	"context"
	"net/http"
	"strconv"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type editGridRuleArea struct {
	editdoc.Area
	List bool `json:"list,omitempty"`
}

// The grid keeps the immutable reader tiles. On entry, undo or recovery it
// loads the bounded sparse revision. An ordinary change needs only its delta.
// A null cell means that coordinate has returned to its original source value.
func (a *App) editGrid(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (any, error) {
	wb := a.currentWorkbook()
	if doc.Revision != req.Revision || wb == nil || req.Book != doc.Base.Source.Book || sourceBookID(wb) != req.Book {
		return nil, checkpoint.ErrConflict
	}
	if len(req.Cells) > formats.EditBatchCells {
		return nil, errEditRequestCapacity
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	calc, err := a.calculationFor(ctx, doc, layout, f)
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]*workbook.CellData)
	changes := doc.Snapshot()
	addresses := req.Cells
	if addresses == nil {
		addresses = make([]editdoc.Address, 0, len(changes))
		for address := range changes {
			addresses = append(addresses, address)
		}
	}
	var modified []editdoc.Address
	for _, addr := range addresses {
		if !layout.Contains(addr) {
			return nil, editfile.ErrTarget
		}
		ref := workbook.ColumnName(addr.Col) + strconv.Itoa(addr.Row)
		if result[addr.Sheet] == nil {
			result[addr.Sheet] = map[string]*workbook.CellData{}
		}
		if _, duplicate := result[addr.Sheet][ref]; duplicate {
			return nil, editfile.ErrTarget
		}
		result[addr.Sheet][ref] = nil
		if _, ok := changes[addr]; ok {
			modified = append(modified, addr)
		}
	}
	var base map[editdoc.Address]editfile.SourceCell
	if layout.XLSX != nil && len(modified) > 0 {
		base, err = layout.Read(ctx, f, modified)
		if err != nil {
			return nil, err
		}
	}
	for _, addr := range modified {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		v := changes[addr].Apply(base[addr].Value)
		ref := workbook.ColumnName(addr.Col) + strconv.Itoa(addr.Row)
		cell := editGridValue(wb, addr, ref, v)
		if v.Kind == "formula" {
			source := base[addr]
			state, value := calc.FormulaResult(addr, source)
			if req.Mode == "projection" {
				// A style overlay may outlive a dependent calculation across a
				// later delta. Only the revision-scoped map holds computed values.
				state, value = "pending", nil
				if source.Value.Kind == "formula" && source.Value.Raw == v.Raw && source.Cache != nil {
					state, value = "saved", source.Cache
				}
			}
			cell = editGridFormula(wb, addr, ref, v, state, value)
		}
		result[addr.Sheet][ref] = cell
	}
	if err := f.Check(); err != nil {
		return nil, err
	}
	calculated := make(map[string]map[string]*workbook.CellData)
	for addr, derived := range calc.Cells {
		ref := workbook.ColumnName(addr.Col) + strconv.Itoa(addr.Row)
		if req.Mode != "projection" && req.Cells != nil {
			if _, asked := result[addr.Sheet][ref]; !asked {
				continue
			}
		}
		dest := calculated
		if req.Mode != "projection" {
			dest = result
		}
		if dest[addr.Sheet] == nil {
			dest[addr.Sheet] = map[string]*workbook.CellData{}
		}
		formula := derived.Formula
		// The number engine deliberately skips styled, empty source cells.
		// Reuse the edit grid's own source read for formulas entered there.
		if source, ok := base[addr]; ok {
			formula = changes[addr].Apply(source.Value)
		}
		dest[addr.Sheet][ref] = editGridFormula(wb, addr, ref, formula, derived.State, &derived.Value)
	}
	for addr, v := range calc.ArrayMembers {
		ref := workbook.ColumnName(addr.Col) + strconv.Itoa(addr.Row)
		dest := calculated
		if req.Mode != "projection" {
			if _, asked := result[addr.Sheet][ref]; req.Cells != nil && !asked {
				continue
			}
			dest = result
		}
		if dest[addr.Sheet] == nil {
			dest[addr.Sheet] = map[string]*workbook.CellData{}
		}
		dest[addr.Sheet][ref] = editGridValue(wb, addr, ref, v)
	}
	if req.Mode != "projection" {
		return result, nil
	}
	var rules []editGridRuleArea
	for _, r := range doc.RuleChanges() {
		rules = append(rules, editGridRuleArea{r.Area, r.Type == "list"})
	}
	return struct {
		Cells      map[string]map[string]*workbook.CellData `json:"cells"`
		Calculated map[string]map[string]*workbook.CellData `json:"calculated,omitempty"`
		Affected   map[string]bool                          `json:"affected,omitempty"`
		Generated  []editfile.GeneratedArea                 `json:"generated,omitempty"`
		Rules      []editGridRuleArea                       `json:"rules,omitempty"`
	}{result, calculated, calc.Affected, calc.Generated, rules}, nil
}

func editGridFormula(wb *workbook.Workbook, addr editdoc.Address, ref string, formula editdoc.Value, state string, value *editdoc.Value) *workbook.CellData {
	v := editdoc.Value{Style: formula.Style, NumberStyle: formula.NumberStyle, BuiltinFormat: formula.BuiltinFormat}
	if value != nil {
		v.Kind, v.Raw = value.Kind, value.Raw
	}
	cell := editGridValue(wb, addr, ref, v)
	cell.HasFormula, cell.Formula = true, formula.Raw
	switch state {
	case "cycle", "pending", "missing":
		cell.Raw, cell.Display, cell.CacheMissing = "", "待重算", true
		if state == "cycle" {
			cell.Display = "循环引用"
		}
		if state == "missing" {
			cell.Display = "缺少保存结果"
		}
	}
	if cell.Type == "inlineStr" {
		cell.Type = "str"
	}
	return cell
}

func editGridValue(wb *workbook.Workbook, addr editdoc.Address, ref string, v editdoc.Value) *workbook.CellData {
	style, _ := strconv.Atoi(v.Style)
	cell := &workbook.CellData{Ref: ref, Col: addr.Col, Style: style, Raw: v.Raw, Display: v.Raw}
	switch v.Kind {
	case "date":
		cell.Type = "d"
	case "number":
		cell.Type = "n"
		if v.BuiltinFormat != 0 {
			cell.Display = wb.FormatNumericID(cell.Raw, v.BuiltinFormat)
			cell.DisplayNumFmt = &v.BuiltinFormat
			cell.ShortDate = wb.GridShortDate(v.BuiltinFormat)
		} else {
			cell.Style, _ = strconv.Atoi(v.NumberFormatStyle())
			wb.PresentNumeric(cell)
			if cell.Style != style && cell.Style >= 0 && cell.Style < len(wb.Styles) {
				id := wb.Styles[cell.Style].NumFmtID
				cell.DisplayNumFmt = &id
				cell.ShortDate = wb.GridShortDate(id)
			}
			cell.Style = style
		}
	case "boolean":
		cell.Type = "b"
	case "error":
		cell.Type = "e"
	case "text":
		cell.Type = "inlineStr"
	}
	return cell
}

// History may refer to a different original than the file currently shown.
// Rebind the visible workbook before painting any draft on its coordinates.
func (a *App) handleEditBase(w http.ResponseWriter, r *http.Request, req editRequest) {
	ctx := r.Context()
	expected := a.currentWorkbook()
	if req.Book != sourceBookID(expected) {
		editError(w, checkpoint.ErrConflict)
		return
	}
	store, err := openEditDocuments()
	if err != nil {
		editError(w, err)
		return
	}
	defer store.Close()
	doc, err := retryCheckpointBusy(ctx, func() (*editdoc.Document, error) { return editdoc.Load(ctx, store, req.Document) })
	if err != nil {
		editError(w, err)
		return
	}
	base := doc.Base
	if req.Mode == "original" {
		base = doc.OriginalBase()
	}
	if req.Revision != 0 && req.Revision != doc.Revision {
		doc.Close()
		editError(w, checkpoint.ErrConflict)
		return
	}
	if err := doc.Close(); err != nil {
		editError(w, err)
		return
	}
	wb, release, err := a.openPinnedWorkbook(base.Source.Path)
	if err != nil {
		editError(w, err)
		return
	}
	defer release()
	f, err := workbook.OpenEditSource(base.Source.Path)
	if err != nil {
		editError(w, err)
		return
	}
	defer f.Close()
	if err := editfile.VerifySource(ctx, f, base.Source.Stamp); err != nil {
		editError(w, err)
		return
	}
	if sourceBookID(wb) != base.Source.Book || !a.installWorkbook(wb, "", expected, true) {
		editError(w, checkpoint.ErrConflict)
		return
	}
	wb.SetDisplayName(base.Name)
	a.closeArchive()
	writeJSON(w, map[string]any{"opened": true, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "fileName": base.Name, "fileSize": f.Size(), "activeTab": wb.ActiveTab, "sheets": wb.Sheets, "source": wb.SourceInfo})
}
