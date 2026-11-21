package server

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type occupiedRun struct{ lo, hi int }

const navigationCacheBudget = 64 << 20

var errNavigationSourceChanged = errors.New("工作簿已切换或索引已变化，请重新定位")

// A dense column is represented by one run. A cold scan uses one descriptor;
// cached jumps search runs rather than walking every cell of a dense region.
func (a *App) columnOccupied(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, column int) ([]occupiedRun, error) {
	key := wb.Fingerprint + "|" + idx.SheetPath + "|" + strconv.Itoa(column)
	lookup := func() ([]occupiedRun, bool) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		found, ok := a.navigationCache[key]
		return found, ok
	}
	if found, ok := lookup(); ok {
		return found, nil
	}
	release, err := a.lockQueryRead(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if a.currentWorkbook() != wb || wb.GetCachedIndex(idx.SheetPath) != idx {
		return nil, errNavigationSourceChanged
	}
	if found, ok := lookup(); ok {
		return found, nil
	}
	var positions iter.Seq[int] = func(yield func(int) bool) {
		for i := 0; i < idx.RowCount(); i++ {
			if !yield(i) {
				return
			}
		}
	}
	var occupied []occupiedRun
	scanned := 0
	err = wb.VisitRowsContext(ctx, idx, positions, column, column, func(row workbook.RowData) error {
		if scanned&255 == 0 && a.currentWorkbook() != wb {
			return errNavigationSourceChanged
		}
		scanned++
		for _, cell := range row.Cells {
			if cell.Col == column && (cell.HasFormula || cell.Raw != "" || cell.Display != "") {
				n := len(occupied)
				if n > 0 && occupied[n-1].hi+1 == row.Number {
					occupied[n-1].hi = row.Number
				} else {
					occupied = append(occupied, occupiedRun{row.Number, row.Number})
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	bytes := cap(occupied) * 2 * (strconv.IntSize / 8)
	if bytes <= navigationCacheBudget && a.wb == wb {
		used := 0
		for _, runs := range a.navigationCache {
			used += cap(runs) * 2 * (strconv.IntSize / 8)
		}
		for k, runs := range a.navigationCache {
			if used+bytes <= navigationCacheBudget && len(a.navigationCache) < 16 {
				break
			}
			used -= cap(runs) * 2 * (strconv.IntSize / 8)
			delete(a.navigationCache, k)
		}
		a.navigationCache[key] = occupied
	}
	return occupied, nil
}

func runBoundaryJump(runs []occupiedRun, start, dir, lo, hi int) int {
	if (dir != 1 && dir != -1) || hi < lo {
		return start
	}
	start = max(lo, min(hi, start))
	i := sort.Search(len(runs), func(i int) bool { return runs[i].hi >= start })
	if i < len(runs) && runs[i].lo <= start {
		if dir > 0 {
			if start < runs[i].hi {
				return min(hi, runs[i].hi)
			}
			i++
		} else {
			if start > runs[i].lo {
				return max(lo, runs[i].lo)
			}
			i--
			if i >= 0 {
				return max(lo, runs[i].hi)
			}
			return lo
		}
	} else if dir < 0 {
		i--
		if i >= 0 {
			return max(lo, runs[i].hi)
		}
		return lo
	}
	if i < len(runs) {
		return min(hi, runs[i].lo)
	}
	return hi
}

// boundaryJump implements Excel's useful Ctrl+Arrow behavior within the
// actual used area, not the million trailing empty rows or 16384 empty cols.
// occupied MUST be sorted and contains each position at most once.
func boundaryJump(occupied []int, start, dir, lo, hi int) int {
	if dir != 1 && dir != -1 {
		return start
	}
	if hi < lo {
		return start
	}
	if start < lo {
		start = lo
	}
	if start > hi {
		start = hi
	}
	at := sort.SearchInts(occupied, start)
	present := at < len(occupied) && occupied[at] == start
	if present {
		if dir > 0 {
			i := at
			for i+1 < len(occupied) && occupied[i+1] == occupied[i]+1 {
				i++
			}
			if i > at {
				return occupied[i]
			}
			if i+1 < len(occupied) {
				return occupied[i+1]
			}
			return hi
		}
		i := at
		for i > 0 && occupied[i-1] == occupied[i]-1 {
			i--
		}
		if i < at {
			return occupied[i]
		}
		if i > 0 {
			return occupied[i-1]
		}
		return lo
	}
	if dir > 0 {
		if at < len(occupied) {
			return occupied[at]
		}
		return hi
	}
	if at > 0 {
		return occupied[at-1]
	}
	return lo
}

func (a *App) handleNavigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, 409, "尚未打开工作簿")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeError(w, 404, "工作表不存在")
		return
	}
	col, row, err := workbook.ParseCellRef(r.URL.Query().Get("ref"))
	if err != nil {
		writeError(w, 400, "单元格地址无效")
		return
	}
	dir := strings.ToLower(r.URL.Query().Get("direction"))
	if dir != "left" && dir != "right" && dir != "up" && dir != "down" {
		writeError(w, 400, "方向无效")
		return
	}
	releaseCache := a.lockCacheRead()
	defer releaseCache()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, 409, "请先打开该工作表建立索引")
		return
	}
	targetRow, targetCol := row, col
	if dir == "up" || dir == "down" {
		if idx.RowCount() > formats.LargeDataRows {
			writeError(w, http.StatusBadRequest, "超大数据模式关闭纵向 Ctrl+方向键扫描；请使用地址框或 Ctrl+Home / Ctrl+End 定位")
			return
		}
		releaseCache()
		occupied, err := a.columnOccupied(r.Context(), wb, idx, col)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			if errors.Is(err, errNavigationSourceChanged) {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeError(w, 500, "定位列数据失败："+err.Error())
			return
		}
		d := -1
		if dir == "down" {
			d = 1
		}
		targetRow = runBoundaryJump(occupied, row, d, 1, max(1, idx.MaxRow))
	} else {
		// Reading exactly one row once is faster than requesting each cell
		// from JavaScript. The fast decoder skips values outside the range.
		occupied := []int{}
		at := idx.FindRowOffset(row)
		if at < idx.RowCount() && idx.RowAt(at).Number == row {
			data, err := wb.ReadRowsByIndicesContext(r.Context(), idx, []int{at}, 1, max(1, idx.MaxCol))
			if err != nil {
				if r.Context().Err() != nil {
					return
				}
				writeError(w, 500, "定位行数据失败："+err.Error())
				return
			}
			if len(data) > 0 {
				for _, cell := range data[0].Cells {
					if cell.HasFormula || cell.Raw != "" || cell.Display != "" {
						occupied = append(occupied, cell.Col)
					}
				}
			}
		}
		d := -1
		if dir == "right" {
			d = 1
		}
		targetCol = boundaryJump(occupied, col, d, 1, max(1, idx.MaxCol))
	}
	if a.currentWorkbook() != wb {
		writeError(w, 409, "工作簿已切换，请重新定位")
		return
	}
	writeJSON(w, map[string]any{"sheet": meta.Name, "ref": fmt.Sprintf("%s%d", workbook.ColumnName(targetCol), targetRow), "row": targetRow, "col": targetCol})
}
