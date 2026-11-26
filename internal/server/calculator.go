package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/calculator"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Shared across browser sessions; heavy selection scans do not occupy the
// viewport/query slots or block independent arithmetic requests.
var calculatorScanSlots = make(chan struct{}, 2)

type calculatorOperand struct {
	ID          string              `json:"id,omitempty"`
	Kind        string              `json:"kind"`
	Value       string              `json:"value"`
	Raw         string              `json:"raw,omitempty"`
	Display     string              `json:"display,omitempty"`
	File        string              `json:"file,omitempty"`
	WorkbookKey string              `json:"workbookKey,omitempty"`
	Sheet       string              `json:"sheet,omitempty"`
	Ref         string              `json:"ref,omitempty"`
	Ranges      []selectionStatRect `json:"ranges,omitempty"`
	Filtered    bool                `json:"filtered,omitempty"`
	Total       int64               `json:"total,omitempty"`
	Numeric     int64               `json:"numeric,omitempty"`
	Text        int64               `json:"text,omitempty"`
	Blank       int64               `json:"blank,omitempty"`
	TextNumbers int64               `json:"textNumbers,omitempty"`
	Notice      string              `json:"notice,omitempty"`
	CapturedAt  string              `json:"capturedAt"`
	Detail      string              `json:"detail,omitempty"`
}
type calculatorCaptureRequest struct {
	Kind              string              `json:"kind"`
	Raw               string              `json:"raw,omitempty"`
	WorkbookKey       string              `json:"workbookKey,omitempty"`
	Sheet             string              `json:"sheet,omitempty"`
	Ref               string              `json:"ref,omitempty"`
	Ranges            []selectionStatRect `json:"ranges,omitempty"`
	FilterID          string              `json:"filterId,omitempty"`
	ViewID            string              `json:"viewId,omitempty"`
	ExcludeThroughRow int                 `json:"excludeThroughRow,omitempty"`
}

func decodeCalculator(w http.ResponseWriter, r *http.Request, value any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	d.DisallowUnknownFields()
	return d.Decode(value)
}
func (a *App) handleCalculatorEvaluate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req calculator.Request
	if decodeCalculator(w, r, &req) != nil {
		writeError(w, 400, "计算参数无效")
		return
	}
	result, err := calculator.Evaluate(r.Context(), req)
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, 422, err.Error())
		}
		return
	}
	writeJSON(w, result)
}
func calculatorNumber(raw string, typed bool) (*big.Rat, bool) {
	raw = strings.TrimSpace(raw)
	// A source date such as 1/2 is not a rational input. Internal result
	// fractions are accepted only by the arithmetic engine, never guessed here.
	if strings.Contains(raw, "/") {
		return nil, false
	}
	percent := strings.HasSuffix(raw, "%")
	if percent {
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "%"))
	}
	if !typed && !analysis.IsAccountingNumericLiteral(raw) {
		return nil, false
	}
	v, ok := analysis.AccountingDecimal(raw)
	if ok && percent {
		v.Quo(v, big.NewRat(100, 1))
	}
	return v, ok
}
func calcCellNumber(cell workbook.CellData) (*big.Rat, bool) {
	if cell.CacheMissing || cell.Type == "e" || cell.Type == "b" || cell.Raw == "" {
		return nil, false
	}
	return calculatorNumber(cell.Raw, cell.Type == "n" || cell.Type == "")
}

func (a *App) handleCalculatorCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req calculatorCaptureRequest
	if decodeCalculator(w, r, &req) != nil {
		writeError(w, 400, "取数参数无效")
		return
	}
	result := calculatorOperand{Kind: req.Kind, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if req.Kind == "manual" {
		if len(req.Raw) > 512 {
			writeError(w, 422, "手输金额过长，请拆分输入")
			return
		}
		v, ok := calculatorNumber(req.Raw, true)
		if !ok {
			writeError(w, 422, "请输入有效金额；支持千位分隔、会计负数及百分数")
			return
		}
		result.Value = v.RatString()
		result.Raw = req.Raw
		result.Display = req.Raw
		writeJSON(w, result)
		return
	}
	if req.Kind != "cell" && req.Kind != "sum" {
		writeError(w, 400, "请选择单格或选区合计")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey == "" || req.WorkbookKey != wb.Fingerprint {
		writeError(w, 409, "工作簿已切换，请重新取数")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeError(w, 404, "工作表不存在")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, 409, "请等待工作表索引完成后取数")
		return
	}
	result.File = workbookDisplayName(wb)
	result.WorkbookKey = wb.Fingerprint
	result.Sheet = meta.Name
	result.Notice = wb.SourceInfo.ArchiveNotice
	if wb.SourceInfo.Incomplete && result.Notice == "" {
		result.Notice = wb.SourceInfo.Notice
	}
	if req.Kind == "cell" {
		col, row, err := workbook.ParseCellRef(req.Ref)
		if err != nil || col > formats.ExcelColumns || row > workbook.MaxRowsForIndex(idx) {
			writeError(w, 400, "单元格地址无效")
			return
		}
		position := idx.FindRowOffset(row)
		if position >= idx.RowCount() || idx.RowAt(position).Number != row {
			writeError(w, 422, "该格为空白，未加入计算")
			return
		}
		rows, err := wb.ReadRowsContext(r.Context(), idx, position, 1, col, col)
		if err != nil {
			writeError(w, 500, "读取单元格失败："+err.Error())
			return
		}
		var cell workbook.CellData
		for _, c := range rows[0].Cells {
			if c.Col == col {
				cell = c
				break
			}
		}
		if cell.CacheMissing {
			writeError(w, 422, "该公式没有保存结果，未加入计算")
			return
		}
		if cell.Type == "e" {
			writeError(w, 422, "该格为 Excel 错误值，未加入计算")
			return
		}
		v, ok := calcCellNumber(cell)
		if !ok {
			writeError(w, 422, "该格不是可计算金额；空白、编号及非数值文本不作0")
			return
		}
		result.Ref = workbook.ColumnName(col) + fmt.Sprint(row)
		result.Value = v.RatString()
		result.Raw = cell.Raw
		result.Display = cell.Display
		result.Numeric = 1
		result.Total = 1
		if cell.Type != "n" && cell.Type != "" {
			result.TextNumbers = 1
		}
	} else {
		select {
		case calculatorScanSlots <- struct{}{}:
			defer func() { <-calculatorScanSlots }()
		case <-r.Context().Done():
			return
		}
		selection, err := a.selectionSnapshot(wb, idx, meta.Path, req.FilterID, req.ViewID)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		defer selection.close()
		result.Filtered = req.FilterID != ""
		result.Ranges = req.Ranges
		collector := calculatorCollector{sum: new(big.Rat)}
		statReq := selectionStatRequest{Sheet: req.Sheet, FilterID: req.FilterID, ViewID: req.ViewID, ExcludeThroughRow: req.ExcludeThroughRow, Ranges: req.Ranges}
		if err := scanCalculatorSelection(r.Context(), wb, idx, selection.filter, statReq, &collector, selection.visible); err != nil {
			if r.Context().Err() == nil {
				writeError(w, 422, err.Error())
			}
			return
		}
		if collector.errors > 0 || collector.missing > 0 {
			writeError(w, 422, fmt.Sprintf("选区含%d个错误值、%d个缺少缓存的公式；未生成不完整合计", collector.errors, collector.missing))
			return
		}
		if collector.number == 0 {
			writeError(w, 422, "选区没有可计算数值，未将空白或文本当作0")
			return
		}
		result.Value = collector.sum.RatString()
		result.Raw = calculator.ExactText(collector.sum)
		result.Display = result.Raw
		result.Total = collector.total
		result.Numeric = collector.number
		result.Text = collector.nonEmpty - collector.number
		result.Blank = collector.total - collector.nonEmpty
		result.TextNumbers = collector.textNumbers
	}
	if a.currentWorkbook() != wb {
		writeError(w, 409, "工作簿已切换，本次取数已放弃")
		return
	}
	writeJSON(w, result)
}

type calculatorCollector struct {
	formulaBook                                           *workbook.Workbook // non-nil for formula reference semantics
	firstFault                                            error
	sum                                                   *big.Rat
	minimum, maximum                                      *big.Rat
	trackExtrema                                          bool
	total, nonEmpty, number, errors, missing, textNumbers int64
}

func (c *calculatorCollector) add(row workbook.RowData) error {
	for _, cell := range row.Cells {
		if cell.Raw == "" && !cell.HasFormula {
			continue
		}
		c.nonEmpty++
		if cell.CacheMissing {
			c.missing++
			if c.firstFault == nil {
				c.firstFault = errors.New("#VALUE! 公式缺少保存结果：" + cell.Ref)
			}
			continue
		}
		if cell.Type == "e" {
			c.errors++
			if c.firstFault == nil {
				c.firstFault = fmt.Errorf("%s: %s", cell.Raw, cell.Ref)
			}
			continue
		}
		var v *big.Rat
		var ok bool
		if c.formulaBook != nil {
			v, ok = formulaCellNumber(c.formulaBook, cell)
		} else {
			v, ok = calcCellNumber(cell)
		}
		if !ok {
			continue
		}
		c.sum.Add(c.sum, v)
		if c.trackExtrema && (c.minimum == nil || v.Cmp(c.minimum) < 0) {
			c.minimum = new(big.Rat).Set(v)
		}
		if c.trackExtrema && (c.maximum == nil || v.Cmp(c.maximum) > 0) {
			c.maximum = new(big.Rat).Set(v)
		}
		c.number++
		if cell.Type != "n" && cell.Type != "" {
			c.textNumbers++
		}
	}
	return nil
}

// Four million cells for an explicit contiguous selection. Multi-area
// selections reuse the existing deduplicated, 400k-cell selection planner.
func scanCalculatorSelection(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, filter *FilterResult, req selectionStatRequest, c *calculatorCollector, views ...*sheetRowView) error {
	if req.ViewID != "" && (len(views) != 1 || views[0] == nil || views[0].isClosed() || views[0].data.id != req.ViewID) {
		return errors.New("计算选区的可见行视图已失效，请重新选择")
	}
	if len(req.Ranges) == 1 {
		span := req.Ranges[0]
		c1, r1, e1 := workbook.ParseCellRef(span.Start)
		c2, r2, e2 := workbook.ParseCellRef(span.End)
		if e1 != nil || e2 != nil || c1 > formats.ExcelColumns || c2 > formats.ExcelColumns || r1 > workbook.MaxRowsForIndex(idx) || r2 > workbook.MaxRowsForIndex(idx) {
			return errors.New("选区地址无效")
		}
		if c1 > c2 {
			c1, c2 = c2, c1
		}
		var positions iter.Seq[int]
		if len(views) == 1 && views[0] != nil {
			view, err := newSelectionRowView(idx, filter, req.ExcludeThroughRow, views...)
			if err != nil {
				return err
			}
			numbers, err := view.rows(r1, r2, span.VisualStart, span.VisualEnd, 4_000_000/(c2-c1+1))
			if err != nil {
				return err
			}
			c.total = int64(len(numbers)) * int64(c2-c1+1)
			picked := numbers[:0]
			for _, number := range numbers {
				if err := ctx.Err(); err != nil {
					return err
				}
				i := idx.FindRowOffset(number)
				if i < idx.RowCount() && idx.RowAt(i).Number == number {
					picked = append(picked, i)
				}
			}
			sort.Ints(picked)
			positions = func(yield func(int) bool) {
				for _, i := range picked {
					if ctx.Err() != nil || !yield(i) {
						return
					}
				}
			}
		} else if filter == nil {
			if r1 > r2 {
				r1, r2 = r2, r1
			}
			c.total = int64(r2-r1+1) * int64(c2-c1+1)
			lo, hi := idx.FindRowOffset(r1), idx.FindRowOffset(r2+1)
			positions = func(yield func(int) bool) {
				for i := lo; i < hi; i++ {
					if !yield(i) {
						return
					}
				}
			}
		} else {
			if req.ExcludeThroughRow < 0 || req.ExcludeThroughRow > 30 {
				return errors.New("冻结行参数无效")
			}
			pinned1, pinned2 := r1 <= req.ExcludeThroughRow, r2 <= req.ExcludeThroughRow
			h1, h2, v1, v2 := 0, -1, span.VisualStart, span.VisualEnd
			switch {
			case pinned1 && pinned2:
				h1, h2 = min(r1, r2), max(r1, r2)
				v1, v2 = 0, -1
			case pinned1 != pinned2:
				if pinned1 {
					h1, h2 = r1, req.ExcludeThroughRow
					v1, v2 = 1, span.VisualEnd
				} else {
					h1, h2 = r2, req.ExcludeThroughRow
					v1, v2 = 1, span.VisualStart
				}
			default:
				if v1 > v2 {
					v1, v2 = v2, v1
				}
			}
			if pinned1 != pinned2 && v2 < v1 {
				return errors.New("筛选选区已失效，请重新选择")
			}
			if v2 >= v1 && v1 < 1 {
				return errors.New("筛选选区已失效，请重新选择")
			}
			count := max(0, v2-v1+1) + max(0, h2-h1+1)
			c.total = int64(count) * int64(c2-c1+1)
			if c.total <= 0 || c.total > 4000000 {
				return errors.New("单选区合计最多400万格，请分段取数")
			}
			// Store only selected positions. A tiny selection on a billion-row
			// source must not allocate a source-sized bitset or scan all rows.
			picked := make([]int, 0, count)
			mark := func(i int) { picked = append(picked, i) }
			if h1 > 0 {
				for i := idx.FindRowOffset(h1); i < idx.RowCount() && idx.RowAt(i).Number <= h2; i++ {
					mark(i)
				}
			}
			if v2 >= v1 {
				view, err := newSelectionRowView(idx, filter, req.ExcludeThroughRow)
				if err != nil {
					return err
				}
				if v2 > view.count() {
					return errors.New("筛选选区已失效，请重新选择")
				}
				for visual := v1; visual <= v2; visual++ {
					if err := ctx.Err(); err != nil {
						return err
					}
					i := filter.Rows.At(view.underlying(visual - 1))
					if i < 0 || i >= idx.RowCount() {
						return errors.New("筛选结果无效")
					}
					mark(i)
				}
			}
			sort.Ints(picked)
			positions = func(yield func(int) bool) {
				previous := -1
				for _, i := range picked {
					if ctx.Err() != nil {
						return
					}
					if i == previous {
						continue
					}
					previous = i
					if !yield(i) {
						return
					}
				}
			}
		}
		if c.total <= 0 || c.total > 4000000 {
			return errors.New("单选区合计最多400万格，请分段取数")
		}
		return wb.VisitRowsContext(ctx, idx, positions, c1, c2, c.add)
	}
	selected, total, err := makeStatRows(req, idx, filter, views...)
	if err != nil {
		return err
	}
	c.total = total
	groups := map[statColInterval][]int{}
	for row, spans := range selected {
		i := idx.FindRowOffset(row)
		if i >= idx.RowCount() || idx.RowAt(i).Number != row {
			continue
		}
		for _, span := range spans {
			groups[span] = append(groups[span], i)
		}
	}
	for span, positions := range groups {
		sort.Ints(positions)
		sequence := func(yield func(int) bool) {
			for _, i := range positions {
				if !yield(i) {
					return
				}
			}
		}
		if err := wb.VisitRowsContext(ctx, idx, sequence, span.lo, span.hi, c.add); err != nil {
			return err
		}
	}
	return nil
}
