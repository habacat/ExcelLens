package reconcile

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"slices"
	"sync"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type RelationQuery struct {
	Keys         []Pair `json:"keys"`
	LeftColumns  []int  `json:"leftColumns"`
	RightColumns []int  `json:"rightColumns"`
}

// Counts can reach 10^18 with the existing billion-row source ceiling. Keep
// their HTTP representation exact even where JavaScript numbers cannot.
type RelationCounts struct {
	LeftRecords          int64 `json:"leftRecords,string"`
	RightRecords         int64 `json:"rightRecords,string"`
	LeftInvalid          int64 `json:"leftInvalid,string"`
	RightInvalid         int64 `json:"rightInvalid,string"`
	LeftDuplicateGroups  int64 `json:"leftDuplicateGroups,string"`
	RightDuplicateGroups int64 `json:"rightDuplicateGroups,string"`
	ManyToManyGroups     int64 `json:"manyToManyGroups,string"`
	MatchingGroups       int64 `json:"matchingGroups,string"`
	LeftMatched          int64 `json:"leftMatched,string"`
	RightMatched         int64 `json:"rightMatched,string"`
	LeftOnly             int64 `json:"leftOnly,string"`
	RightOnly            int64 `json:"rightOnly,string"`
	InnerRows            int64 `json:"innerRows,string"`
	LeftJoinRows         int64 `json:"leftJoinRows,string"`
}

func (c RelationCounts) Rows(mode string) (int64, error) {
	switch mode {
	case "left":
		return c.LeftJoinRows, nil
	case "inner":
		return c.InnerRows, nil
	case "left_matched":
		return c.LeftMatched, nil
	case "left_unmatched":
		return c.LeftOnly, nil
	case "right_unmatched":
		return c.RightOnly, nil
	case "left_invalid":
		return c.LeftInvalid, nil
	case "right_invalid":
		return c.RightInvalid, nil
	default:
		return 0, errors.New("关联提取方式无效")
	}
}

type RelationRow struct {
	Sequence     int64   `json:"sequence,string"`
	Left         *Record `json:"left,omitempty"`
	Right        *Record `json:"right,omitempty"`
	Status       string  `json:"status"`
	Reason       string  `json:"reason,omitempty"`
	RightMatches int64   `json:"rightMatches,string"` // zero unless Status is matched
}

type RelationPreview struct {
	Rows      []RelationRow `json:"rows"`
	Total     int64         `json:"total,string"`
	Truncated bool          `json:"truncated"`
}

// Relation owns only sorted position orders, never source handles or values.
// Callers must retain their immutable source leases while using the plan.
// Close may race with a running operation: each operation borrows both orders.
type Relation struct {
	mu       sync.RWMutex
	closed   bool
	dir      string
	query    RelationQuery
	sources  [2]Source
	orders   [2]*workbook.RowOrder
	digests  [2][32]byte
	counts   RelationCounts
	previews [len(relationModes)][]relationPair
}

func relationColumns(q RelationQuery, right bool, keysOnly bool) []int {
	columns := []int{}
	if !keysOnly {
		columns = slices.Clone(q.LeftColumns)
		if right {
			columns = slices.Clone(q.RightColumns)
		}
	}
	for _, pair := range q.Keys {
		column := pair.Left
		if right {
			column = pair.Right
		}
		columns = append(columns, column)
	}
	slices.Sort(columns)
	return slices.Compact(columns)
}

func RelationColumns(q RelationQuery, right bool) []int { return relationColumns(q, right, false) }

func ValidateRelation(q RelationQuery, left, right Source) error {
	if err := Validate(Query{Keys: q.Keys}, left, right); err != nil {
		return err
	}
	if len(q.LeftColumns)+len(q.RightColumns) == 0 {
		return errors.New("请选择关联结果字段")
	}
	for side, source := range []Source{left, right} {
		if source.Check == nil || source.SourceRows > formats.ExternalRows || source.Info.Total > formats.ExternalRows {
			return errors.New("关联来源缺少身份核对或超过源记录容量")
		}
		if len(source.Info.Fields) > formats.ExcelColumns {
			return errors.New("关联来源字段超出容量")
		}
		fields := make(map[int]bool, len(source.Info.Fields))
		for _, field := range source.Info.Fields {
			if field.Column < 1 || field.Column > formats.ExcelColumns || fields[field.Column] {
				return errors.New("关联来源字段列号无效或重复")
			}
			fields[field.Column] = true
		}
		columns := q.LeftColumns
		if side == 1 {
			columns = q.RightColumns
		}
		if len(columns) > formats.RelationColumns {
			return fmt.Errorf("每侧最多选择%d个结果字段", formats.RelationColumns)
		}
		seen := map[int]bool{}
		for _, column := range columns {
			if column < 1 || column > formats.ExcelColumns || seen[column] || !slices.ContainsFunc(source.Info.Fields, func(field Field) bool { return field.Column == column }) {
				return errors.New("关联结果字段不存在或重复")
			}
			seen[column] = true
		}
	}
	return nil
}

func PrepareRelation(ctx context.Context, dir string, query RelationQuery, left, right Source, progress Progress) (*Relation, error) {
	if err := ValidateRelation(query, left, right); err != nil {
		return nil, err
	}
	p := &Relation{dir: dir, query: RelationQuery{Keys: slices.Clone(query.Keys), LeftColumns: slices.Clone(query.LeftColumns), RightColumns: slices.Clone(query.RightColumns)}, sources: [2]Source{left, right}}
	for side := range p.sources {
		p.sources[side].Info.Fields = slices.Clone(p.sources[side].Info.Fields)
	}
	ready := false
	defer func() {
		if !ready {
			_ = p.Close()
		}
	}()
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	for side := range p.orders {
		var err error
		p.orders[side], p.digests[side], err = p.sort(ctx, dir, side, progress)
		if err != nil {
			return nil, err
		}
	}
	var candidates [len(relationModes)]relationCandidates
	err := p.groups(ctx, p.orders, func(group relationGroup) error {
		l, r := int64(group.count[0]), int64(group.count[1])
		p.counts.LeftRecords += l
		p.counts.RightRecords += r
		if group.key[0] == 0 {
			p.counts.LeftInvalid += l
			p.counts.RightInvalid += r
			return p.collectPreviews(ctx, group, &candidates)
		}
		if l > 1 {
			p.counts.LeftDuplicateGroups++
		}
		if r > 1 {
			p.counts.RightDuplicateGroups++
		}
		switch {
		case l == 0:
			p.counts.RightOnly += r
		case r == 0:
			p.counts.LeftOnly += l
		default:
			p.counts.MatchingGroups++
			p.counts.LeftMatched += l
			p.counts.RightMatched += r
			p.counts.InnerRows += l * r
			if l > 1 && r > 1 {
				p.counts.ManyToManyGroups++
			}
		}
		if progress != nil {
			done := p.counts.LeftRecords + p.counts.RightRecords
			if done&4095 == 0 {
				progress("预检关联数量", done, int64(left.Info.Total)+int64(right.Info.Total))
			}
		}
		return p.collectPreviews(ctx, group, &candidates)
	})
	if err != nil {
		return nil, err
	}
	for i := range p.previews {
		p.previews[i] = candidates[i].rows
	}
	p.counts.LeftJoinRows = p.counts.InnerRows + p.counts.LeftOnly + p.counts.LeftInvalid
	if p.counts.LeftRecords != int64(left.Info.Total) || p.counts.RightRecords != int64(right.Info.Total) {
		return nil, errors.New("关联预检记录数与来源不一致")
	}
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	ready = true
	return p, nil
}

func (p *Relation) Query() RelationQuery {
	return RelationQuery{Keys: slices.Clone(p.query.Keys), LeftColumns: slices.Clone(p.query.LeftColumns), RightColumns: slices.Clone(p.query.RightColumns)}
}

func (p *Relation) Counts() RelationCounts { return p.counts }

func (p *Relation) Sources() [2]SourceInfo {
	var out [2]SourceInfo
	for side, source := range p.sources {
		out[side] = source.Info
		columns := relationColumns(p.query, side == 1, false)
		out[side].Fields = nil
		for _, field := range source.Info.Fields {
			if slices.Contains(columns, field.Column) {
				out[side].Fields = append(out[side].Fields, field)
			}
		}
	}
	return out
}

func (p *Relation) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	orders := p.orders
	p.mu.Unlock()
	var err error
	for _, order := range orders {
		if order != nil {
			err = errors.Join(err, order.Close())
		}
	}
	return err
}

func (p *Relation) borrow() ([2]*workbook.RowOrder, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var orders [2]*workbook.RowOrder
	if p.closed {
		return orders, errors.New("关联预检已释放，请重新运行")
	}
	for side := range orders {
		var err error
		orders[side], err = p.orders[side].Retain()
		if err != nil {
			if orders[0] != nil {
				_ = orders[0].Close()
			}
			return [2]*workbook.RowOrder{}, err
		}
	}
	return orders, nil
}

func (p *Relation) check(ctx context.Context) error {
	for _, source := range p.sources {
		if err := source.Check(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func relationHash(h hash.Hash, position int, key string) {
	var raw [12]byte
	binary.LittleEndian.PutUint32(raw[:], uint32(position))
	binary.LittleEndian.PutUint64(raw[4:], uint64(len(key)))
	_, _ = h.Write(raw[:])
	_, _ = h.Write([]byte(key))
}

func projectRelationRecord(record Record, columns []int) Record {
	out := Record{Position: record.Position, Number: record.Number, Values: make(map[int]Value, len(columns))}
	for _, column := range columns {
		if value, exists := record.Values[column]; exists {
			out.Values[column] = value
		}
	}
	return out
}

func relationKey(record Record, keys []Pair, right bool) (string, string, error) {
	key, reason, err := checkedKey(record, Query{Keys: keys}, right)
	if err != nil || reason != "" {
		return key, reason, err
	}
	for _, pair := range keys {
		column := pair.Left
		if right {
			column = pair.Right
		}
		if record.Values[column].Truncated {
			return invalidRecordKey(record.Position, right), "关联键原文未完整读取", nil
		}
	}
	return key, "", nil
}
