package workbook

import (
	"context"
	"errors"
	"strings"

	"excellens/internal/formats"
)

// Next identifies the next immutable source-index position, not a worksheet
// row or byte offset. A capped result may stop inside a row: its terminal flags
// describe the result, while Next stays at the last complete-record boundary.
type SearchCheckpoint struct {
	Next      int            `json:"next"`
	Matches   []SearchResult `json:"matches"`
	Complete  bool           `json:"complete"`
	Truncated bool           `json:"truncated"`
}

type SearchCheckpointSave func(SearchCheckpoint) error

// Persistent searches cannot first accumulate hundreds of huge cell values and
// only discover the journal limit at the next 4096-record checkpoint. Budget
// each retained hit before append; ordinary searches keep their prior behavior.
type searchHitBudget struct {
	stream  *searchStream
	hits    int64
	enabled bool
	bytes   int64
}

func newSearchHitBudget(save SearchCheckpointSave, previous []SearchResult, stream *searchStream) (*searchHitBudget, error) {
	budget := &searchHitBudget{enabled: save != nil && stream == nil, stream: stream}
	for _, hit := range previous {
		if err := budget.add(hit); err != nil {
			return nil, err
		}
	}
	return budget, nil
}

func (budget *searchHitBudget) add(hit SearchResult) error {
	if !budget.enabled {
		return nil
	}
	// Six bytes per UTF-8 input byte bounds JSON escaping; the fixed part
	// covers field names, coordinates and each hit's possible block header.
	weight := (int64(len(hit.Sheet))+int64(len(hit.Ref))+int64(len(hit.Formula))+int64(len(hit.Value)))*6 + 256
	if weight > int64(formats.CheckpointSearchBytes)-budget.bytes {
		return errors.New("搜索命中超过恢复容量，请缩小搜索范围")
	}
	budget.bytes += weight
	return nil
}

// In streaming mode the callback owns each hit before the scanner advances.
type searchStream struct {
	hit func(SearchResult) error
	row func(int, bool) error
}

func (b *searchHitBudget) collect(out *[]SearchResult, h SearchResult) error {
	b.hits++
	if b.stream != nil {
		return b.stream.hit(h)
	}
	if err := b.add(h); err != nil {
		return err
	}
	*out = append(*out, h)
	return nil
}
func (w *Workbook) StreamSearch(ctx context.Context, idx *SheetIndex, sheet, query, mode string, start int, hit func(SearchResult) error, row func(int, bool) error, progress func(int, int, []SearchResult)) error {
	if hit == nil || row == nil {
		return errors.New("搜索缺少结果接收器")
	}
	_, err := w.scanSearch(ctx, idx, sheet, query, mode, 500, SearchCheckpoint{Next: start}, nil, progress, &searchStream{hit, row})
	return err
}

func cloneSearchCheckpoint(state SearchCheckpoint) SearchCheckpoint {
	state.Matches = append([]SearchResult{}, state.Matches...)
	return state
}

func prepareSearchCheckpoint(idx *SheetIndex, sheet, mode string, limit int, state SearchCheckpoint) (SearchCheckpoint, error) {
	if idx == nil || state.Next < 0 || state.Next > idx.RowCount() || len(state.Matches) > limit || state.Truncated && !state.Complete || state.Complete && !state.Truncated && state.Next != idx.RowCount() || state.Truncated && (len(state.Matches) != limit || state.Next >= idx.RowCount()) || !state.Complete && len(state.Matches) >= limit {
		return SearchCheckpoint{}, errors.New("搜索恢复游标与来源索引或结果容量不一致")
	}
	previousRow, previousColumn := 0, 0
	upper := state.Next - 1
	if state.Truncated {
		upper++
	}
	for _, hit := range state.Matches {
		column, row, err := ParseCellRef(hit.Ref)
		if err != nil || hit.Sheet != sheet || row != hit.Row || column != hit.Col || hit.Col < 1 || hit.Col > formats.ExcelColumns || hit.Row < previousRow || hit.Row == previousRow && (mode == "advanced" || hit.Col <= previousColumn) {
			return SearchCheckpoint{}, errors.New("搜索恢复命中坐标或顺序无效")
		}
		at := idx.FindRowOffset(hit.Row)
		if at > upper || at >= idx.RowCount() || idx.RowAt(at).Number != hit.Row {
			return SearchCheckpoint{}, errors.New("搜索恢复命中不属于已确认的源记录")
		}
		previousRow, previousColumn = hit.Row, hit.Col
	}
	return cloneSearchCheckpoint(state), nil
}

func saveSearchCheckpoint(state SearchCheckpoint, save SearchCheckpointSave, force bool, stream *searchStream) error {
	if stream != nil {
		return stream.row(state.Next, state.Complete)
	}
	if save != nil && (force || state.Next%formats.CheckpointSearchRows == 0) {
		return save(cloneSearchCheckpoint(state))
	}
	return nil
}

// SearchSheetResumable reuses the existing parsers/predicates. Its caller must
// bind source, index and query identities before supplying a saved cursor.
// Only an explicit query scans rows; opening or listing never resumes work.
func (w *Workbook) SearchSheetResumable(ctx context.Context, idx *SheetIndex, sheet, query, mode string, limit int, previous SearchCheckpoint, save SearchCheckpointSave, progress func(int, int, []SearchResult)) (SearchCheckpoint, error) {
	return w.scanSearch(ctx, idx, sheet, query, mode, limit, previous, save, progress, nil)
}
func (w *Workbook) scanSearch(ctx context.Context, idx *SheetIndex, sheet, query, mode string, limit int, previous SearchCheckpoint, save SearchCheckpointSave, progress func(int, int, []SearchResult), stream *searchStream) (SearchCheckpoint, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchCheckpoint{}, errors.New("搜索内容不能为空")
	}
	if mode == "advanced" {
		if limit < 1 || limit > 10000 {
			return SearchCheckpoint{}, errors.New("结果数量限制无效")
		}
	} else {
		query = strings.ToLower(query)
		if mode != "value" && mode != "reference" {
			mode = "formula"
		}
		if limit <= 0 || limit > 5000 {
			limit = 500
		}
	}
	state, err := prepareSearchCheckpoint(idx, sheet, mode, limit, previous)
	if err != nil {
		return SearchCheckpoint{}, err
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if state.Complete {
		return state, nil
	}
	if mode == "advanced" {
		return w.searchAdvancedResumable(ctx, idx, sheet, query, limit, state, save, progress, stream)
	}
	return w.searchSimpleResumable(ctx, idx, sheet, query, mode, limit, state, save, progress, stream)
}

func searchContext(cancel <-chan struct{}) (context.Context, context.CancelFunc) {
	if cancel == nil {
		return context.Background(), func() {}
	}
	ctx, stop := context.WithCancel(context.Background())
	select {
	case <-cancel:
		stop()
	default:
	}
	go func() {
		select {
		case <-cancel:
			stop()
		case <-ctx.Done():
		}
	}()
	return ctx, stop
}
