package server

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/workbook"
)

// Quick status-bar statistics use the immutable worksheet index, not the
// virtualized DOM. A cell in overlapping selections is counted exactly once.
// Limits prevent accidental whole-sheet selections from monopolizing the UI.
const (
	maxSelectionRects = 256
	maxQuickStatCells = 400_000
)

type selectionStatRect struct {
	Start       string `json:"start"`
	End         string `json:"end"`
	VisualStart int    `json:"visualStart,omitempty"`
	VisualEnd   int    `json:"visualEnd,omitempty"`
}
type selectionStatRequest struct {
	Edit              *selectionStatEdit  `json:"edit,omitempty"`
	Sheet             string              `json:"sheet"`
	FilterID          string              `json:"filterId,omitempty"`
	ViewID            string              `json:"viewId,omitempty"`
	ExcludeThroughRow int                 `json:"excludeThroughRow,omitempty"`
	Ranges            []selectionStatRect `json:"ranges"`
}
type selectionStatEdit struct {
	Document editdoc.Key `json:"document"`
	Revision uint64      `json:"revision,string"`
	Book     string      `json:"book"`
}
type selectionStatResult struct {
	Total    int64  `json:"total"`
	NonEmpty int64  `json:"nonEmpty"`
	Number   int64  `json:"number"`
	Sum      string `json:"sum"`
	Average  string `json:"average"`
	Minimum  string `json:"minimum,omitempty"`
	Maximum  string `json:"maximum,omitempty"`
	Ranges   int    `json:"ranges"`
	Filtered bool   `json:"filtered"`
}
type statColInterval struct{ lo, hi int }

// Each row's intervals are merged BEFORE reading any XML. This is the
// important correctness property for Ctrl+drag and overlapping ranges.
func mergeStatIntervals(items []statColInterval) []statColInterval {
	sort.Slice(items, func(i, j int) bool {
		if items[i].lo == items[j].lo {
			return items[i].hi < items[j].hi
		}
		return items[i].lo < items[j].lo
	})
	out := items[:0]
	for _, it := range items {
		n := len(out)
		if n > 0 && it.lo <= out[n-1].hi+1 {
			if it.hi > out[n-1].hi {
				out[n-1].hi = it.hi
			}
		} else {
			out = append(out, it)
		}
	}
	return out
}

// Selection coordinates are source row numbers for ordinary views and visual
// row indexes for filtered/sorted views. Only actual filtered rows may be read.
func makeStatRows(req selectionStatRequest, idx *workbook.SheetIndex, filter *FilterResult, views ...*sheetRowView) (map[int][]statColInterval, int64, error) {
	if req.ViewID != "" && (len(views) != 1 || views[0] == nil || views[0].isClosed() || views[0].data.id != req.ViewID) {
		return nil, 0, errors.New("选区的可见行视图已失效，请重新选择")
	}
	if len(req.Ranges) == 0 || len(req.Ranges) > maxSelectionRects {
		return nil, 0, errors.New("选区数量必须在1至256之间")
	}
	if req.ExcludeThroughRow < 0 || req.ExcludeThroughRow > 30 {
		return nil, 0, errors.New("冻结行参数无效")
	}
	view, err := newSelectionRowView(idx, filter, req.ExcludeThroughRow, views...)
	if err != nil {
		return nil, 0, err
	}
	selected := make(map[int][]statColInterval)
	var budget int64
	for _, sel := range req.Ranges {
		c1, r1, e1 := workbook.ParseCellRef(sel.Start)
		c2, r2, e2 := workbook.ParseCellRef(sel.End)
		if e1 != nil || e2 != nil {
			return nil, 0, errors.New("选区地址无效")
		}
		if c1 > c2 {
			c1, c2 = c2, c1
		}
		width := int64(c2 - c1 + 1)
		numbers, err := view.rows(r1, r2, sel.VisualStart, sel.VisualEnd, int((maxQuickStatCells-budget)/width))
		if err != nil {
			if errors.Is(err, errSelectionLimit) {
				return nil, 0, errors.New("选区超过40万格，请缩小选区后查看即时统计")
			}
			return nil, 0, err
		}
		budget += int64(len(numbers)) * width
		interval := statColInterval{lo: c1, hi: c2}
		for _, row := range numbers {
			selected[row] = append(selected[row], interval)
		}
	}
	var unique int64
	for row, intervals := range selected {
		merged := mergeStatIntervals(intervals)
		selected[row] = merged
		for _, it := range merged {
			unique += int64(it.hi - it.lo + 1)
		}
	}
	return selected, unique, nil
}

// Exact for terminating decimal expansions; average for recurring fractions
// is rounded to 12 places for the compact status bar (the underlying rational
// arithmetic is exact). It never passes monetary data through float64.
func selectionRatText(r *big.Rat) string {
	if r == nil {
		return ""
	}
	if r.IsInt() {
		return r.Num().String()
	}
	den := new(big.Int).Set(r.Denom())
	two, five := 0, 0
	for new(big.Int).Mod(den, big.NewInt(2)).Sign() == 0 {
		den.Div(den, big.NewInt(2))
		two++
	}
	for new(big.Int).Mod(den, big.NewInt(5)).Sign() == 0 {
		den.Div(den, big.NewInt(5))
		five++
	}
	if den.Cmp(big.NewInt(1)) == 0 {
		digits := two
		if five > digits {
			digits = five
		}
		if digits <= 100 && r.Num().BitLen() < 4096 {
			scaled := new(big.Int).Mul(r.Num(), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil))
			scaled.Quo(scaled, r.Denom())
			negative := scaled.Sign() < 0
			if negative {
				scaled.Abs(scaled)
			}
			txt := scaled.String()
			for len(txt) <= digits {
				txt = "0" + txt
			}
			if digits > 0 {
				txt = txt[:len(txt)-digits] + "." + txt[len(txt)-digits:]
			}
			txt = strings.TrimRight(strings.TrimRight(txt, "0"), ".")
			if negative && txt != "0" {
				txt = "-" + txt
			}
			return txt
		}
	}
	// Preserve small nonzero averages instead of rounding them visibly to zero.
	places := len(r.Denom().String()) - len(new(big.Int).Abs(r.Num()).String()) + 14
	if places < 12 {
		places = 12
	}
	if places > 80 {
		places = 80
	}
	result := strings.TrimRight(strings.TrimRight(r.FloatString(places), "0"), ".")
	return result + " ≈"
}

func (a *App) handleSelectionStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req selectionStatRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, 400, "选区统计参数无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, 409, "请先打开工作簿")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeError(w, 404, "找不到指定工作表")
		return
	}
	release, err := a.lockQueryRead(r.Context())
	if err != nil {
		return
	}
	defer release()
	if a.currentWorkbook() != wb {
		writeError(w, 409, "工作簿已切换")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, 409, "请先打开并索引工作表")
		return
	}
	var filter *FilterResult
	if req.FilterID != "" && req.ViewID == "" {
		filter = a.filterSnapshot(req.FilterID)
		defer filter.close()
		if filter == nil || filter.SheetPath != meta.Path {
			writeError(w, 409, "筛选结果已失效")
			return
		}
	}
	var visible *sheetRowView
	if req.ViewID != "" {
		var err error
		visible, err = a.rowViewSnapshot(req.ViewID, wb, idx, meta.Path, req.FilterID)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		defer visible.close()
	}
	selected, total, err := makeStatRows(req, idx, filter, visible)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	res := selectionStatResult{Total: total, Ranges: len(req.Ranges), Filtered: req.FilterID != "", Sum: "0", Average: "0"}
	var projection *editGridProjection
	if req.Edit != nil {
		projection, err = a.selectionProjection(r.Context(), wb, req, selected)
		if err != nil {
			editError(w, err)
			return
		}
	}
	// Only non-empty source rows need I/O. Group same column spans so two
	// distant Ctrl-selections never force reading every intervening column.
	groups := make(map[statColInterval][]int)
	for sourceRow, spans := range selected {
		i := idx.FindRowOffset(sourceRow)
		if i >= idx.RowCount() || idx.RowAt(i).Number != sourceRow {
			continue
		}
		for _, span := range spans {
			groups[span] = append(groups[span], i)
		}
	}
	keys := make([]statColInterval, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].lo == keys[j].lo {
			return keys[i].hi < keys[j].hi
		}
		return keys[i].lo < keys[j].lo
	})
	sum := new(big.Rat)
	min, max := new(big.Rat), new(big.Rat)
	add := func(cell workbook.CellData) {
		if cell.Raw == "" && !cell.HasFormula {
			return
		}
		res.NonEmpty++
		numeric := sourceCellNumber(wb, cell)
		if numeric == nil {
			return
		}
		if res.Number == 0 || numeric.Cmp(min) < 0 {
			min.Set(numeric)
		}
		if res.Number == 0 || numeric.Cmp(max) > 0 {
			max.Set(numeric)
		}
		res.Number++
		sum.Add(sum, numeric)
	}
	// Revision patches also include formerly empty cells and rows. Count each
	// overlay once below and skip the underlying source coordinate during the scan.
	var patched, calculated map[string]*workbook.CellData
	var generated editfile.Calculation
	if projection != nil {
		patched, calculated = projection.Cells[req.Sheet], projection.Calculated[req.Sheet]
		for _, area := range projection.Generated {
			if area.Start.Sheet == req.Sheet {
				generated.Generated = append(generated.Generated, area)
			}
		}
	}
	addBase := func(cell workbook.CellData, row int) {
		if projection != nil {
			address := editdoc.Address{Sheet: req.Sheet, Row: row, Col: cell.Col}
			if (cell.HasFormula && projection.Affected[req.Sheet]) || generated.GeneratedStatus(address) != "" {
				cell.Raw = ""
			}
		}
		add(cell)
	}
	for _, span := range keys {
		positions := groups[span]
		sort.Ints(positions)
		readErr := wb.VisitRowsContext(r.Context(), idx, slices.Values(positions), span.lo, span.hi, func(row workbook.RowData) error {
			for _, cell := range row.Cells {
				if cell.Col < span.lo || cell.Col > span.hi {
					continue
				}
				if projection == nil {
					add(cell)
				} else {
					ref := cell.Ref
					if ref == "" {
						ref = workbook.ColumnName(cell.Col) + strconv.Itoa(row.Number)
					}
					if patched[ref] == nil && calculated[ref] == nil {
						addBase(cell, row.Number)
					}
				}
			}
			return nil
		})
		if readErr != nil {
			if !errors.Is(readErr, context.Canceled) {
				writeError(w, 500, "读取选区失败："+readErr.Error())
			}
			return
		}
	}
	for ref, cell := range patched {
		if cell != nil && calculated[ref] == nil {
			_, row, _ := workbook.ParseCellRef(ref)
			addBase(*cell, row)
		}
	}
	for _, cell := range calculated {
		add(*cell)
	}
	if res.Number > 0 {
		res.Sum = selectionRatText(sum)
		res.Average = selectionRatText(new(big.Rat).Quo(sum, new(big.Rat).SetInt64(res.Number)))
		res.Minimum = selectionRatText(min)
		res.Maximum = selectionRatText(max)
	}
	writeJSON(w, res)
}

func (a *App) selectionProjection(ctx context.Context, wb *workbook.Workbook, req selectionStatRequest, rows map[int][]statColInterval) (*editGridProjection, error) {
	edit := req.Edit
	if edit.Book != sourceBookID(wb) {
		return nil, checkpoint.ErrConflict
	}
	store, err := openEditDocuments()
	if err != nil {
		return nil, err
	}
	defer store.Close()
	doc, err := retryCheckpointBusy(ctx, func() (*editdoc.Document, error) { return editdoc.Load(ctx, store, edit.Document) })
	if err != nil {
		return nil, err
	}
	defer doc.Close()
	if doc.Revision != edit.Revision || editViewBook(doc.ID, doc.Base) != edit.Book {
		return nil, checkpoint.ErrConflict
	}
	layout, err := editfile.ParseLayout(doc.Base.Layout, doc.Base.Source.Format)
	if err != nil {
		return nil, err
	}
	layout, err = a.edits.documentEditLayout(ctx, doc, layout, nil)
	if err != nil {
		return nil, err
	}
	include := func(addr editdoc.Address) bool {
		if addr.Sheet != req.Sheet {
			return false
		}
		for _, span := range rows[addr.Row] {
			if span.lo <= addr.Col && addr.Col <= span.hi {
				return true
			}
		}
		return false
	}
	return a.readEditGrid(ctx, doc, a.edits.withSourceCells(layout, doc), editRequest{Book: edit.Book, Revision: edit.Revision, Mode: "projection"}, include)
}
