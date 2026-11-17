package analysis

import (
	"errors"
	"fmt"
	"time"
)

// CellChange is one write in a transaction. A transaction either validates
// all cells and commits once or changes nothing.
type CellChange struct {
	Row   int    `json:"row"`
	Col   int    `json:"col"`
	Value string `json:"value"`
}

const MaxBatchCells = 20000

// AppendRows uses the actual persisted size; adding space must never edit an
// existing cell, even when a browser's worksheet summary is out of date.
func (s *Sheet) AppendRows(count int) error {
	if count < 1 || count > 1000 || len(s.Rows)+count > MaxSheetRows {
		return errors.New("追加行数超限：每次1至1000行，编辑表最多10,000行")
	}
	for i := 0; i < count; i++ {
		s.Rows = append(s.Rows, []string{})
	}
	s.Revision++
	s.Updated = time.Now().UTC().Format(time.RFC3339)
	return nil
}

func (s *Sheet) ApplyEdits(changes []CellChange) ([]CellChange, error) {
	if len(changes) < 1 || len(changes) > MaxBatchCells {
		return nil, errors.New("每次编辑须包含1至20,000个单元格")
	}
	for _, e := range changes {
		if e.Row < 0 || e.Row >= MaxSheetRows || e.Col < 0 || e.Col >= len(s.Columns) || !ValidCellText(e.Value) {
			return nil, fmt.Errorf("单元格位置或文本长度超限（行%d，列%d）", e.Row+1, e.Col+1)
		}
	}
	before := make([]CellChange, 0, len(changes))
	seen := make(map[[2]int]struct{}, len(changes))
	for _, e := range changes {
		key := [2]int{e.Row, e.Col}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		old := ""
		if e.Row < len(s.Rows) && e.Col < len(s.Rows[e.Row]) {
			old = s.Rows[e.Row][e.Col]
		}
		before = append(before, CellChange{Row: e.Row, Col: e.Col, Value: old})
	}
	for _, e := range changes {
		for len(s.Rows) <= e.Row {
			s.Rows = append(s.Rows, []string{})
		}
		for len(s.Rows[e.Row]) <= e.Col {
			s.Rows[e.Row] = append(s.Rows[e.Row], "")
		}
		s.Rows[e.Row][e.Col] = e.Value
	}
	s.Revision++
	s.Updated = time.Now().UTC().Format(time.RFC3339)
	return before, nil
}
