package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"

	"fmt"
	"net/http"
	"strconv"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/i18n"
	"excellens/internal/recipe"
	"excellens/internal/reconcile"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

type recipeTargetSchema struct {
	Info      reconcile.SourceInfo
	Fields    []recipe.Field
	Stamp     string
	Sampled   int
	Header    int
	Scope     *sourceview.Scope
	Truncated bool
}

func recipeDigest(value recipe.Recipe) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:])
}

// Uniqueness belongs to the entire source header, including columns unused by
// the saved query. Beyond the bounded header read, uniqueness is not proven.
func recipeSourceFields(info reconcile.SourceInfo) []recipe.Field {
	headers := sourceHeaderIdentities(info)
	out := make([]recipe.Field, 0, min(len(info.Fields), analysis.MaxSourceColumns))
	for i, f := range info.Fields[:min(len(info.Fields), analysis.MaxSourceColumns)] {
		out = append(out, recipe.Field{ID: "C" + strconv.Itoa(f.Column), Column: f.Column, Name: f.Name, HeaderHash: f.HeaderHash, HeaderLoaded: f.HeaderLoaded && f.RawName != "", HeaderUnique: headers[i].Unique, Truncated: f.Truncated})
	}
	return out
}

// At most eight records are previewed; replay guards the complete input.
func (a *App) recipeTargetSchema(ctx context.Context, wb *workbook.Workbook, ref reconcileSourceRef, captured ...*analysisSourceSnapshot) (recipeTargetSchema, error) {
	if ref.Book != sourceBookID(wb) {
		return recipeTargetSchema{}, i18n.Error("service.recipesPreview.targetSourceAnalysisSchemeSwitched")
	}
	scratch := strings.HasPrefix(ref.Source, "scratch:")
	header := ref.HeaderRow
	if scratch {
		ref.HeaderRow = 0
		header = 1
	} else if header < 1 {
		return recipeTargetSchema{}, i18n.Error("service.recipesPreview.headerRowNumbersAnalyzedScheme")
	}
	source, err := a.buildReconcileSource(ctx, wb, ref, nil)
	if err != nil {
		return recipeTargetSchema{}, err
	}
	out := recipeTargetSchema{Info: source.Info, Fields: recipeSourceFields(source.Info), Stamp: wb.Fingerprint, Header: header}
	out.Truncated = len(out.Info.Fields) > len(out.Fields)
	var scoped analysisSource
	if len(captured) > 0 && captured[0] != nil {
		base := captured[0]
		scoped, err = a.sourceQuery(ctx, wb, analysis.Query{Source: ref.Source, HeaderRow: header, SourceScope: base.canonicalScope()}, nil, base)
		if err != nil {
			return out, err
		}
		defer scoped.close()
		out.Scope = base.canonicalScope()
		out.Stamp = scoped.stamp()
		out.Info.Total = scoped.total
		// Uniqueness was established against the complete header above. Only
		// now project original column IDs, retaining blank selected columns.
		available := make(map[int]bool, len(scoped.fields))
		for _, field := range scoped.fields {
			available[field.Column] = true
		}
		if area := out.Scope.Selection; area != nil {
			for column := len(out.Fields) + 1; column <= area.EndColumn; column++ {
				out.Info.Fields = append(out.Info.Fields, sourceField(column, "", false))
				out.Fields = append(out.Fields, recipe.Field{ID: "C" + strconv.Itoa(column), Column: column, Name: workbook.ColumnName(column) + " 列"})
			}
		}
		fields := out.Fields[:0:0]
		infoFields := out.Info.Fields[:0:0]
		for _, field := range out.Fields {
			if available[field.Column] {
				fields = append(fields, field)
				infoFields = append(infoFields, out.Info.Fields[field.Column-1])
			}
		}
		out.Fields, out.Info.Fields = fields, infoFields
		out.Truncated = scoped.truncatedFields
	}
	slots := make(map[int]int, len(out.Fields))
	for i, field := range out.Fields {
		slots[field.Column] = i
	}
	observe := func(format string, cell workbook.CellData, numeric bool) {
		if slot, exists := slots[cell.Col]; exists {
			out.Fields[slot].Mask |= uint16(recipeCellMask(format, cell, numeric))
		}
	}
	if scratch {
		workspace, err := a.loadAnalysis(wb)
		if err != nil {
			return out, err
		}
		sheet := workspace.Sheet(strings.TrimPrefix(ref.Source, "scratch:"))
		if sheet == nil {
			return out, i18n.Error("service.recipesPreview.targetEditingTableDoesNot")
		}
		stamp := analysisSheetStamp(sheet)
		if stamp != source.Info.Stamp {
			return out, i18n.Error("service.recipesPreview.targetEditTableChangedWhen")
		}
		out.Stamp = stamp
		for _, row := range sheet.Rows[:min(len(sheet.Rows), 8)] {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			out.Sampled++
			for col, value := range row[:min(len(row), len(out.Fields))] {
				observe("scratch", workbook.CellData{Col: col + 1, Raw: value, Type: "s"}, analysis.IsNumericLiteral(value))
			}
		}
	} else if scoped.lease != nil {
		columns := make([]int, len(out.Fields))
		for i, field := range out.Fields {
			columns[i] = field.Column
		}
		rows := func(yield func(workbook.SourceRowReference) bool) {
			seen := 0
			for row := range scoped.lease.rowsAfter(header) {
				if !yield(row) {
					return
				}
				seen++
				if seen == 8 {
					return
				}
			}
		}
		err = wb.VisitRowsByReferencesWithOptionsContext(ctx, scoped.lease.data.index, rows, columns[0], columns[len(columns)-1], workbook.RowReadOptions{FormatValue: true, Columns: columns}, func(row workbook.RowData) error {
			out.Sampled++
			for _, cell := range row.Cells {
				observe(wb.Format, cell, sourceCellNumeric(wb, cell))
			}
			return nil
		})
		if err != nil {
			return out, err
		}
	} else {
		meta, _ := wb.FindSheet(source.Info.Sheet)
		idx := wb.GetCachedIndex(meta.Path)
		if idx == nil {
			return out, i18n.Error("service.recipesPreview.targetIndexBecomeInvalid")
		}
		first := idx.FindRowOffset(header + 1)
		if first < idx.RowCount() {
			rows, err := wb.ReadRowsContext(ctx, idx, first, min(8, idx.RowCount()-first), 1, len(out.Fields))
			if err != nil {
				return out, err
			}
			for _, row := range rows {
				out.Sampled++
				for _, cell := range row.Cells {
					observe(wb.Format, cell, sourceCellNumeric(wb, cell))
				}
			}
		}
	}
	for i := range out.Fields {
		out.Fields[i].Uncertain = recipeTypeMask(out.Fields[i].Mask).uncertain()
	}
	return out, nil
}

func (a *App) handleRecipePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID          string             `json:"id"`
		WorkbookKey string             `json:"workbookKey"`
		Book        string             `json:"book"`
		Target      reconcileSourceRef `json:"target"`
		SourceID    string             `json:"sourceId,omitempty"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeMessage(w, 400, "service.recipesPreview.analysisPlanPreviewParametersInvalid")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.Book != sourceBookID(wb) || req.WorkbookKey != wb.Fingerprint || req.Target.Book != req.Book {
		writeMessage(w, 409, "service.recipesPreview.workbookSwitchedReselectAnalysisPlan")
		return
	}
	value, err := findRecipe(req.ID)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	var captured *analysisSourceSnapshot
	if req.SourceID != "" {
		captured, err = a.analysisSourceSnapshot(req.SourceID, wb)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		defer captured.close()
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	target, err := a.recipeTargetSchema(r.Context(), wb, req.Target, captured)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	if a.currentWorkbook() != wb {
		writeMessage(w, 409, "service.recipesPreview.workbookSwitchedPreviewNotPublished")
		return
	}
	labels := func(fields []recipe.Field) map[string]string {
		out := make(map[string]string, len(fields))
		for _, field := range fields {
			out[field.ID] = recipeTypeMask(field.Mask).label()
		}
		return out
	}
	// Match against the known full header before trimming transport metadata;
	// a very wide source must not send thousands of unusable field entries.
	target.Info.Fields = target.Info.Fields[:len(target.Fields)]
	writeJSON(w, map[string]any{"recipe": value, "digest": recipeDigest(value), "target": target.Info, "stamp": target.Stamp, "sourceScope": target.Scope, "fields": target.Fields, "matches": recipe.Suggest(value.Fields, target.Fields), "expectedTypes": labels(value.Fields), "targetTypes": labels(target.Fields), "sampled": target.Sampled, "sampleOnly": target.Sampled < target.Info.Total, "fieldsTruncated": target.Truncated})
}

func (a *App) handleRecipeRunStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID          string             `json:"id"`
		Digest      string             `json:"digest"`
		WorkbookKey string             `json:"workbookKey"`
		Book        string             `json:"book"`
		Target      reconcileSourceRef `json:"target"`
		Stamp       string             `json:"stamp"`
		Mapping     map[string]string  `json:"mapping"`
		SourceID    string             `json:"sourceId,omitempty"`
	}
	if decodeAnalysis(r, &req) != nil || len(req.Mapping) > analysis.MaxSourceFields || req.Stamp == "" {
		writeMessage(w, 400, "service.recipesPreview.analysisPlanSRunningParameters")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.Book != sourceBookID(wb) || req.WorkbookKey != wb.Fingerprint || req.Target.Book != req.Book {
		writeMessage(w, 409, "service.recipesPreview.workbookSwitchedPreviewTargetSource")
		return
	}
	value, err := findRecipe(req.ID)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	if req.Digest != recipeDigest(value) {
		writeMessage(w, 409, "service.recipesPreview.analysisPlanDefinitionChangedReread")
		return
	}
	var captured *analysisSourceSnapshot
	if req.SourceID != "" {
		captured, err = a.analysisSourceSnapshot(req.SourceID, wb)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		defer captured.close()
	}
	a.cacheMu.RLock()
	target, err := a.recipeTargetSchema(r.Context(), wb, req.Target, captured)
	a.cacheMu.RUnlock()
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	if req.Stamp != target.Stamp || req.Target.Stamp == "" || req.Target.Stamp != target.Info.Stamp {
		writeMessage(w, 409, "service.recipesPreview.analysisPlanTargetOrHeader")
		return
	}
	if err := recipe.ValidateMapping(value.Fields, target.Fields, req.Mapping); err != nil {
		writeFailure(w, 409, err)
		return
	}
	query, output, err := value.Query(req.Target.Source, target.Header, req.Mapping)
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	query.SourceScope = target.Scope
	masks := make(map[int]recipeTypeMask, len(value.Fields))
	for _, field := range value.Fields {
		column, _ := strconv.Atoi(strings.TrimPrefix(req.Mapping[field.ID], "C"))
		masks[column] = recipeTypeMask(field.Mask)
	}
	job, err := a.startAnalysisTask(wb, query, &recipeRunPlan{Stamp: target.Stamp, Masks: masks, RecipeID: value.ID, ExportFormat: output.Format}, captured)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	writeJSON(w, map[string]string{"jobId": job.ID})
}
