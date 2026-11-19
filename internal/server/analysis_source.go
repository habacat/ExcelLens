package server

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strconv"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Fields and source scans share the same strict source snapshot. A sheet source
// never changes the workbook, while editable sheets are snapshotted from JSON.
type analysisSource struct {
	fields          []analysis.Field
	scan            analysis.Scan
	total           int
	stamp           func() string
	truncatedFields bool
	lease           *analysisSourceSnapshot
}

// The observer is opt-in for recipe schema capture and guarded replay. Ordinary
// analysis keeps its existing source values and numeric interpretation.
type sourceCellObserver func(row int, cell workbook.CellData, numeric bool) error

func (a *App) source(wb *workbook.Workbook, source string, header int) (analysisSource, error) {
	return a.sourceObserved(wb, source, header, nil)
}

func (a *App) sourceObserved(wb *workbook.Workbook, source string, header int, observer sourceCellObserver) (analysisSource, error) {
	if strings.HasPrefix(source, "scratch:") {
		a.analysisMu.Lock()
		ws, err := a.loadAnalysis(wb)
		a.analysisMu.Unlock()
		if err != nil {
			return analysisSource{}, err
		}
		s := ws.Sheet(strings.TrimPrefix(source, "scratch:"))
		if s == nil {
			return analysisSource{}, errors.New("编辑表不存在")
		}
		fields := make([]analysis.Field, len(s.Columns))
		for i, name := range s.Columns {
			numeric, nonempty := 0, 0
			for j := 0; j < min(len(s.Rows), 48); j++ {
				if i < len(s.Rows[j]) && strings.TrimSpace(s.Rows[j][i]) != "" {
					nonempty++
					if analysis.IsNumericLiteral(s.Rows[j][i]) {
						numeric++
					}
				}
			}
			fields[i] = analysis.Field{ID: fmt.Sprintf("C%d", i+1), Column: i + 1, Name: name, Numeric: nonempty > 0 && numeric*10 >= nonempty*7}
		}
		scan := func(ctx context.Context, need []string, visit func(analysis.Record) error) error {
			for i, row := range s.Rows {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				vals := make(map[string]analysis.Value, len(need))
				for _, id := range need {
					col, _ := strconv.Atoi(strings.TrimPrefix(id, "C"))
					if col < 1 || col > len(row) {
						continue
					}
					val := row[col-1]
					numeric := analysis.IsNumericLiteral(val)
					if observer != nil {
						if err := observer(i+1, workbook.CellData{Col: col, Raw: val, Display: val, Type: "s"}, numeric); err != nil {
							return err
						}
					}
					vals[id] = analysis.Value{Text: val, Display: val, Numeric: numeric, Textual: !numeric}
				}
				if err := visit(analysis.Record{Number: i + 1, Values: vals}); err != nil {
					return err
				}
			}
			return nil
		}
		// Hash row by row: a backup can restore different contents with the same
		// numeric revision. Avoid encoding a second whole worksheet in memory.
		stamp := func() string { return analysisSheetStamp(s) }
		return analysisSource{fields: fields, scan: scan, total: len(s.Rows), stamp: stamp}, nil
	}
	if !strings.HasPrefix(source, "wb:") {
		return analysisSource{}, errors.New("数据源格式错误")
	}
	meta, ok := wb.FindSheet(strings.TrimPrefix(source, "wb:"))
	if !ok {
		return analysisSource{}, errors.New("源工作表不存在")
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return analysisSource{}, errors.New("源工作表尚未索引，请先选择该表或等待分析任务建立索引")
	}
	if header < 1 || header > idx.MaxRow {
		return analysisSource{}, errors.New("表头行号无效")
	}
	cols := min(idx.MaxCol, analysis.MaxSourceColumns)
	if cols < 1 {
		return analysisSource{}, errors.New("源表没有可分析的列")
	}
	pos := idx.FindRowOffset(header)
	var headerCells []workbook.CellData
	if pos < idx.RowCount() && idx.RowAt(pos).Number == header {
		row, err := wb.ReadRow(idx, pos, 1, cols)
		if err != nil {
			return analysisSource{}, err
		}
		headerCells = row.Cells
	}
	names := make(map[int]string, len(headerCells))
	for _, cell := range headerCells {
		names[cell.Col] = strings.TrimSpace(cell.Display)
	}
	fields := make([]analysis.Field, cols)
	usedNames := make(map[string]bool)
	for i := 0; i < cols; i++ {
		fields[i] = workbookAnalysisField(i+1, names[i+1], usedNames)
	}
	scan := func(ctx context.Context, need []string, visit func(analysis.Record) error) error {
		first := idx.FindRowOffset(header + 1)
		references := func(yield func(workbook.SourceRowReference) bool) {
			for i := first; i < idx.RowCount(); i++ {
				if !yield(workbook.SourceRowReference{Number: idx.RowAt(i).Number, Position: i}) {
					return
				}
			}
		}
		return scanWorkbookAnalysisRows(ctx, wb, idx, iter.Seq[workbook.SourceRowReference](references), need, observer, visit)
	}
	return analysisSource{fields: fields, scan: scan, total: idx.RowCount() - idx.FindRowOffset(header+1), stamp: func() string { return wb.Fingerprint }, truncatedFields: idx.MaxCol > analysis.MaxSourceColumns}, nil
}
func workbookAnalysisField(column int, name string, used map[string]bool) analysis.Field {
	letter := workbook.ColumnName(column)
	if name == "" {
		name = letter + " 列"
	}
	if used[name] {
		base := name + " · " + letter
		name = base
		for n := 2; used[name]; n++ {
			name = base + " (" + strconv.Itoa(n) + ")"
		}
	}
	used[name] = true
	return analysis.Field{ID: "C" + strconv.Itoa(column), Column: column, Name: name}
}

func (a *App) handleAnalysisFields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	source := r.URL.Query().Get("source")
	header := clamp(parseIntDefault(r.URL.Query().Get("headerRow"), 1), 1, formats.ExternalRows)
	release, err := a.acquireAnalysisQuerySlot(r.Context(), source, false)
	if err != nil {
		writeError(w, 408, "读取联合来源已取消")
		return
	}
	defer release()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	s, err := a.sourceRequest(r.Context(), wb, source, header, r.URL.Query().Get("sourceId"))
	if err != nil {
		status := 400
		if strings.Contains(err.Error(), "尚未索引") {
			status = 409
		}
		writeError(w, status, err.Error())
		return
	}
	defer s.close()
	// Sample up to eight actual rows across *all* 256 source columns. The
	// initial preview still transfers only 16 columns, but templates need the
	// numeric typing for fields beyond P. Reading eight rows directly avoids
	// running a whole 256-row scan batch on every schema inspection.
	sampled := make([]analysis.DrillRow, 0, 8)
	if s.lease != nil && s.lease.data.composition != nil {
		sampled, err = compositionAnalysisPreview(r.Context(), &s)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		header = 0
	} else if s.lease != nil {
		sampled, err = scopedAnalysisPreview(r.Context(), wb, &s, header)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
	} else if strings.HasPrefix(source, "wb:") {
		meta, _ := wb.FindSheet(strings.TrimPrefix(source, "wb:"))
		idx := wb.GetCachedIndex(meta.Path)
		start := idx.FindRowOffset(header + 1)
		if start < idx.RowCount() {
			rows, readErr := wb.ReadRowsContext(r.Context(), idx, start, min(8, idx.RowCount()-start), 1, len(s.fields))
			if readErr != nil {
				writeError(w, 500, readErr.Error())
				return
			}
			counts := make([]int, len(s.fields))
			numbers := make([]int, len(s.fields))
			for _, row := range rows {
				first := make([]string, min(16, len(s.fields)))
				for _, c := range row.Cells {
					if c.Col < 1 || c.Col > len(s.fields) {
						continue
					}
					text := c.Raw
					if c.Type != "" && c.Type != "n" {
						text = c.Display
					}
					if strings.TrimSpace(text) != "" {
						counts[c.Col-1]++
						if sourceCellNumeric(wb, c) {
							numbers[c.Col-1]++
						}
					}
					if c.Col <= len(first) {
						first[c.Col-1] = c.Display
					}
				}
				sampled = append(sampled, analysis.DrillRow{Row: row.Number, Cells: first})
			}
			for i := range s.fields {
				s.fields[i].Numeric = counts[i] > 0 && numbers[i]*10 >= counts[i]*7
			}
		}
	} else {
		need := make([]string, 0, min(16, len(s.fields)))
		for _, f := range s.fields[:min(16, len(s.fields))] {
			need = append(need, f.ID)
		}
		err = s.scan(r.Context(), need, func(rec analysis.Record) error {
			cells := make([]string, len(need))
			for i, id := range need {
				v := rec.Values[id]
				if v.Display != "" {
					cells[i] = v.Display
				} else {
					cells[i] = v.Text
				}
			}
			sampled = append(sampled, analysis.DrillRow{Row: rec.Number, Cells: cells})
			if len(sampled) >= 8 {
				return errAnalysisSampleDone
			}
			return nil
		})
		if err != nil && !errors.Is(err, errAnalysisSampleDone) {
			writeError(w, 500, err.Error())
			return
		}
	}
	response := map[string]any{"fields": s.fields, "preview": sampled, "totalRows": s.total, "headerRow": header, "truncatedFields": s.truncatedFields}
	if s.lease != nil {
		if s.lease.data.composition != nil {
			response["composition"] = s.lease.data.composition.data.plan.Definition()
		} else {
			response["sourceScope"] = s.lease.canonicalScope()
		}
	}
	// Replay adopts a schema only after confirming its completed source stamp.
	// Ordinary field previews do not hash the entire saved analysis table.
	if r.URL.Query().Get("stamp") == "1" {
		response["sourceStamp"] = s.stamp()
	}
	writeJSON(w, response)
}

var errAnalysisSampleDone = errors.New("preview complete")
