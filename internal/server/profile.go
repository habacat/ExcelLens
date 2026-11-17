package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/big"
	"net/http"
	"sort"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type profileRequest struct {
	analysisSelectionCoordinates
	WorkbookKey         string `json:"workbookKey,omitempty"`
	Book                string `json:"book,omitempty"`
	BindingViewID       string `json:"bindingViewId,omitempty"`
	BindingFilterID     string `json:"bindingFilterId,omitempty"`
	Sheet               string `json:"sheet"`
	Start               string `json:"start"`
	End                 string `json:"end"`
	FilterID            string `json:"filterId"`
	IncludeDistribution bool   `json:"includeDistribution,omitempty"`
}

type valueFrequency struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}
type profileReport struct {
	profileRequest
	Range           string                        `json:"range"`
	Rows            int                           `json:"rows"`
	FirstColumn     int                           `json:"firstColumn"`
	LastColumn      int                           `json:"lastColumn"`
	Total           int64                         `json:"total"`
	NonEmpty        int64                         `json:"nonEmpty"`
	Blank           int64                         `json:"blank"`
	Number          int64                         `json:"number"`
	Text            int64                         `json:"text"`
	Formulas        int64                         `json:"formulas"`
	CacheMissing    int64                         `json:"cacheMissing"`
	Errors          int64                         `json:"errors"`
	Sum             string                        `json:"sum"`
	Average         string                        `json:"average"`
	Minimum         string                        `json:"minimum"`
	Maximum         string                        `json:"maximum"`
	SumExact        string                        `json:"sumExact"`
	AverageExact    string                        `json:"averageExact"`
	MinimumExact    string                        `json:"minimumExact"`
	MaximumExact    string                        `json:"maximumExact"`
	Unique          int                           `json:"unique"`
	UniqueTruncated bool                          `json:"uniqueTruncated"`
	TopValues       []valueFrequency              `json:"topValues"`
	ErrorRefs       []string                      `json:"errorRefs"`
	MissingRefs     []string                      `json:"missingRefs"`
	Filtered        bool                          `json:"filtered"`
	Warning         string                        `json:"warning,omitempty"`
	Distribution    *analysis.NumericDistribution `json:"distribution,omitempty"`
}

var errProfileCapacity = errors.New("选区内容超过数据洞察容量，请缩小范围或使用流式导出")

// Explicit inspection retains exact original types and one immutable source
// scope. Distribution adds external sorting only when the caller requests it.
func (a *App) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.ProfileRequestBytes))
	var req profileRequest
	if err != nil || decodeSavedConfig(data, &req) != nil || req.ExcludeThroughRow < 0 || req.ExcludeThroughRow > 30 || req.IncludeDistribution && (req.WorkbookKey == "" || req.Book == "" || req.BindingViewID == "") || req.BindingFilterID != "" && req.BindingViewID == "" {
		writeError(w, 400, "分析参数无效，请重新选择范围")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, 409, "请先打开工作簿")
		return
	}
	if req.WorkbookKey != "" && req.WorkbookKey != wb.Fingerprint || req.Book != "" && req.Book != sourceBookID(wb) {
		writeError(w, 409, "分析来源已切换，请重新打开数据洞察")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeError(w, 404, "找不到工作表")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, 409, "请先打开并索引该工作表")
		return
	}
	first, row1, e1 := workbook.ParseCellRef(req.Start)
	last, row2, e2 := workbook.ParseCellRef(req.End)
	if e1 != nil || e2 != nil {
		writeError(w, 400, "请先选择有效的单元格区域")
		return
	}
	first, last = min(first, last), max(first, last)
	if first > idx.MaxCol {
		writeError(w, 400, "选区超出工作表有效列范围")
		return
	}
	last = min(last, idx.MaxCol)
	if req.ViewID != "" && req.BindingViewID != "" && req.ViewID != req.BindingViewID {
		writeError(w, 409, "选区视图已变化，请重新选择")
		return
	}
	snapshot, err := a.selectionSnapshot(wb, idx, meta.Path, req.FilterID, req.ViewID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer snapshot.close()
	var binding selectionSnapshot
	if req.BindingViewID != "" {
		binding, err = a.selectionSnapshot(wb, idx, meta.Path, req.BindingFilterID, req.BindingViewID)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		defer binding.close()
	}
	unpin := a.pinWorkbooks(wb)
	defer unpin()
	current := func() error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		a.mu.RLock()
		valid := a.wb == wb && !a.maintenance && wb.GetCachedIndex(meta.Path) == idx
		for _, bound := range []selectionSnapshot{snapshot, binding} {
			if bound.visible != nil {
				view := a.rowViews[bound.visible.data.id]
				valid = valid && view != nil && view.data == bound.visible.data
			}
		}
		a.mu.RUnlock()
		if !valid {
			return errors.New("分析来源或视图已变化，请重新选择")
		}
		return wb.CheckSourceUnchanged()
	}
	select {
	case a.querySem <- struct{}{}:
		defer func() { <-a.querySem }()
	case <-r.Context().Done():
		return
	}
	if err := current(); err != nil {
		if r.Context().Err() == nil {
			writeError(w, 409, err.Error())
		}
		return
	}
	rows, count, err := profileSelectionRows(idx, snapshot, row1, row2, req.analysisSelectionCoordinates)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if int64(count)*int64(last-first+1) > formats.ProfileCells {
		writeError(w, 400, "当前选区超过400万个单元格，请缩小范围后分析")
		return
	}
	result, err := readProfile(r.Context(), wb, idx, req, rows, count, first, last)
	if err == nil {
		remaining := formats.ProfileResultBytes - 16384
		for _, value := range result.TopValues {
			if !jsonTextFits(value.Value, &remaining) {
				err = errProfileCapacity
				break
			}
		}
	}
	if err == nil {
		data, err = json.Marshal(result)
		if len(data) > formats.ProfileResultBytes {
			err = errProfileCapacity
		}
	}
	if changed := current(); changed != nil {
		if r.Context().Err() == nil {
			writeError(w, 409, changed.Error())
		}
		return
	}
	if err != nil {
		status := 500
		if errors.Is(err, errProfileCapacity) {
			status = 413
		}
		writeError(w, status, "数据分析失败："+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// Typed source ranges intersect the complete filter set in source order.
// Captured visual selections keep paired endpoints and frozen semantics.
// Neither path allocates an array proportional to the selected row count.
func profileSelectionRows(idx *workbook.SheetIndex, snapshot selectionSnapshot, row1, row2 int, coords analysisSelectionCoordinates) (iter.Seq[workbook.SourceRowReference], int, error) {
	visual := coords.ViewID != "" || coords.VisualStart != 0 || coords.VisualEnd != 0 || coords.ExcludeThroughRow != 0
	if visual && snapshot.filter == nil && snapshot.visible == nil {
		return nil, 0, errors.New("可视选区缺少有效筛选结果，请重新选择")
	}
	if coords.ViewID != "" && (snapshot.visible == nil || snapshot.visible.data.id != coords.ViewID) {
		return nil, 0, errors.New("可见行视图已失效")
	}
	if visual || snapshot.filter == nil {
		view, err := newSelectionRowView(idx, snapshot.filter, coords.ExcludeThroughRow, snapshot.visible)
		if err != nil {
			return nil, 0, err
		}
		plan, err := view.plan(row1, row2, coords.VisualStart, coords.VisualEnd)
		if err != nil {
			return nil, 0, err
		}
		if plan.Len() < 1 {
			return nil, 0, errSelectionLimit
		}
		return plan.Rows(), plan.Len(), nil
	}
	lo, hi := min(row1, row2), max(row1, row2)
	if lo < 1 || hi > workbook.MaxRowsForIndex(idx) || snapshot.filter.Rows.SourceRows() != idx.RowCount() {
		return nil, 0, errors.New("分析范围或筛选来源无效")
	}
	set := snapshot.filter.Rows.Set()
	start, end := set.LowerBound(idx.FindRowOffset(lo)), set.LowerBound(idx.FindRowOffset(hi+1))
	return func(yield func(workbook.SourceRowReference) bool) {
		for at := start; at < end; at++ {
			position := set.At(at)
			if !yield(workbook.SourceRowReference{Number: idx.RowAt(position).Number, Position: position}) {
				return
			}
		}
	}, end - start, nil
}

func readProfile(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, req profileRequest, rows iter.Seq[workbook.SourceRowReference], count, first, last int) (*profileReport, error) {
	var sourceBytes int64
	for ref := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ref.Position < 0 {
			continue
		}
		size, err := sourceRowByteLength(idx, ref.Position)
		if err != nil {
			return nil, err
		}
		if size > formats.ProfileSourceRowBytes || size > formats.ProfileSourceBytes-sourceBytes {
			return nil, errProfileCapacity
		}
		sourceBytes += size
	}
	report := &profileReport{profileRequest: req, Range: fmt.Sprintf("%s:%s", req.Start, req.End), Rows: count, FirstColumn: first, LastColumn: last, Total: int64(count) * int64(last-first+1), Filtered: req.FilterID != "", TopValues: []valueFrequency{}, ErrorRefs: []string{}, MissingRefs: []string{}}
	freq := make(map[string]int64)
	var frequencyBytes int64
	limited := false
	textBytes := int64(0)
	sum := new(big.Rat)
	var minimum, maximum *big.Rat
	scan := func(ctx context.Context, emit func(workbook.KeyRecord) error) error {
		return wb.VisitRowsByReferencesWithOptionsContext(ctx, idx, rows, first, last, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true}, func(row workbook.RowData) error {
			for _, cell := range row.Cells {
				if err := ctx.Err(); err != nil {
					return err
				}
				for _, text := range []string{cell.Raw, cell.Display, cell.Formula} {
					if int64(len(text)) > formats.ProfileTextBytes-textBytes {
						return errProfileCapacity
					}
					textBytes += int64(len(text))
				}
				if cell.Raw == "" && !cell.HasFormula {
					continue
				}
				report.NonEmpty++
				if cell.HasFormula {
					report.Formulas++
					if cell.CacheMissing {
						report.CacheMissing++
						if len(report.MissingRefs) < 18 {
							report.MissingRefs = append(report.MissingRefs, cell.Ref)
						}
					}
				}
				if cell.Type == "e" {
					report.Errors++
					if len(report.ErrorRefs) < 18 {
						report.ErrorRefs = append(report.ErrorRefs, cell.Ref)
					}
				}
				n := sourceCellNumber(wb, cell)
				if n != nil {
					report.Number++
					sum.Add(sum, n)
					if minimum == nil {
						minimum, maximum = new(big.Rat).Set(n), new(big.Rat).Set(n)
					} else {
						if n.Cmp(minimum) < 0 {
							minimum.Set(n)
						}
						if n.Cmp(maximum) > 0 {
							maximum.Set(n)
						}
					}
					if emit != nil {
						if err := emit(workbook.KeyRecord{Position: int(report.Number - 1), Key: n.RatString()}); err != nil {
							return err
						}
					}
				} else {
					report.Text++
				}
				if cell.CacheMissing {
					continue
				}
				v := cell.Display
				if freq[v] > 0 {
					freq[v]++
				} else if len(freq) < formats.ProfileDistinct && frequencyBytes+int64(len(v))+64 <= formats.ProfileFrequencyBytes {
					freq[strings.Clone(v)] = 1
					frequencyBytes += int64(len(v)) + 64
				} else {
					limited = true
				}
			}
			return nil
		})
	}
	var err error
	if req.IncludeDistribution {
		var distribution *analysis.SortedDistribution
		err = workbook.SortRationalKeyStream(ctx, wb.CacheDir, int(report.Total), formats.ProfileSortMemoryBytes, scan, func(record workbook.KeyRecord) error {
			if distribution == nil {
				var err error
				distribution, err = analysis.NewSortedDistribution(report.Number, minimum, maximum, formats.ProfileHistogramBins)
				if err != nil {
					return err
				}
			}
			value, ok := new(big.Rat).SetString(record.Key)
			if !ok {
				return errors.New("精确排序结果无效")
			}
			return distribution.AddSorted(value)
		}, nil)
		if err == nil {
			if distribution == nil {
				distribution, err = analysis.NewSortedDistribution(0, nil, nil, formats.ProfileHistogramBins)
			}
			if err == nil {
				report.Distribution, err = distribution.Finish()
			}
		}
	} else {
		err = scan(ctx, nil)
	}
	if err != nil {
		return nil, err
	}
	report.Blank = report.Total - report.NonEmpty
	report.Sum = selectionRatText(sum)
	report.SumExact = analysis.ExactDecimalString(sum)
	if report.Number > 0 {
		average := new(big.Rat).Quo(sum, big.NewRat(report.Number, 1))
		report.Average = selectionRatText(average)
		report.Minimum = selectionRatText(minimum)
		report.Maximum = selectionRatText(maximum)
		report.AverageExact = analysis.ExactDecimalString(average)
		report.MinimumExact = analysis.ExactDecimalString(minimum)
		report.MaximumExact = analysis.ExactDecimalString(maximum)
	}
	report.Unique, report.UniqueTruncated = len(freq), limited
	for value, count := range freq {
		report.TopValues = append(report.TopValues, valueFrequency{value, count})
	}
	sort.Slice(report.TopValues, func(i, j int) bool {
		if report.TopValues[i].Count == report.TopValues[j].Count {
			return report.TopValues[i].Value < report.TopValues[j].Value
		}
		return report.TopValues[i].Count > report.TopValues[j].Count
	})
	if len(report.TopValues) > formats.ProfileTopValues {
		report.TopValues = report.TopValues[:formats.ProfileTopValues]
	}
	if limited {
		report.Warning = "常见值及不同值数量只覆盖已纳入的值；数值统计覆盖完整选区。"
	}
	return report, nil
}
