package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"iter"
	"slices"
	"strings"
	"sync"
	"time"

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

func (p *Relation) sort(ctx context.Context, dir string, side int, progress Progress) (*workbook.RowOrder, [32]byte, error) {
	source := p.sources[side]
	columns := relationColumns(p.query, side == 1, true)
	h := sha256.New()
	scanned := 0
	lastReport := time.Now()
	order, err := workbook.BuildRowOrder(ctx, dir, source.SourceRows, func(ctx context.Context, add func(int) error) error {
		return workbook.SortKeyStream(ctx, dir, source.SourceRows, formats.QuerySortMemoryBytes/2, func(ctx context.Context, emit func(workbook.KeyRecord) error) error {
			err := source.Scan(ctx, columns, func(record Record) error {
				if record.Position < 0 || record.Position >= source.SourceRows {
					return errors.New("关联源位置无效")
				}
				key, _, err := relationKey(projectRelationRecord(record, columns), p.query.Keys, side == 1)
				if err != nil {
					return err
				}
				if err := emit(workbook.KeyRecord{Position: record.Position, Key: key}); err != nil {
					return err
				}
				scanned++
				if progress != nil && (scanned == source.Info.Total || scanned&4095 == 0 && time.Since(lastReport) >= 200*time.Millisecond) {
					label := "读取左侧关联键"
					if side == 1 {
						label = "读取右侧关联键"
					}
					progress(label, int64(scanned), int64(source.Info.Total))
					lastReport = time.Now()
				}
				return ctx.Err()
			})
			if err == nil && scanned != source.Info.Total {
				err = errors.New("关联来源未完整读取")
			}
			return err
		}, func(record workbook.KeyRecord) error {
			relationHash(h, record.Position, record.Key)
			return add(record.Position)
		}, func(stage string, done, total int) {
			if progress != nil {
				label := "左侧"
				if side == 1 {
					label = "右侧"
				}
				progress(label+"："+stage, int64(done), int64(total))
			}
		})
	})
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return order, digest, err
}

type relationHead struct {
	key, reason string
	index       int
}

type relationGroup struct {
	key, reason string
	start       [2]int
	count       [2]int
}

func (p *Relation) heads(ctx context.Context, order *workbook.RowOrder, side int) iter.Seq2[relationHead, error] {
	return func(yield func(relationHead, error) bool) {
		source, h := p.sources[side], sha256.New()
		columns := relationColumns(p.query, side == 1, true)
		expected, read, previous := -1, 0, ""
		positions := func(emit func(int) bool) {
			for position := range order.Iter() {
				expected = position
				if !emit(position) {
					return
				}
			}
		}
		err := source.Read(ctx, positions, columns, func(record Record) error {
			if record.Position != expected {
				return errors.New("关联源位置与排序行序不一致")
			}
			key, reason, err := relationKey(projectRelationRecord(record, columns), p.query.Keys, side == 1)
			if err != nil {
				return err
			}
			if key < previous {
				return errors.New("关联键在预检后发生变化，请重新运行")
			}
			previous = key
			relationHash(h, record.Position, key)
			head := relationHead{key: key, reason: reason, index: read}
			read++
			if !yield(head, nil) {
				return errStop
			}
			return ctx.Err()
		})
		if errors.Is(err, errStop) {
			return
		}
		if err == nil && (read != source.Info.Total || !slices.Equal(h.Sum(nil), p.digests[side][:])) {
			err = errors.New("关联键或记录数与排序快照不一致，请重新运行")
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			yield(relationHead{}, err)
		}
	}
}

func (p *Relation) groups(ctx context.Context, orders [2]*workbook.RowOrder, visit func(relationGroup) error) error {
	lNext, lStop := iter.Pull2(p.heads(ctx, orders[0], 0))
	defer lStop()
	rNext, rStop := iter.Pull2(p.heads(ctx, orders[1], 1))
	defer rStop()
	l, err, lok := lNext()
	if err != nil {
		return err
	}
	r, err, rok := rNext()
	if err != nil {
		return err
	}
	for lok || rok {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := l.key
		if !lok || rok && r.key < key {
			key = r.key
		}
		group := relationGroup{key: key, start: [2]int{l.index, r.index}}
		for lok && l.key == key {
			group.count[0]++
			group.reason = l.reason
			l, err, lok = lNext()
			if err != nil {
				return err
			}
		}
		for rok && r.key == key {
			group.count[1]++
			group.reason = r.reason
			r, err, rok = rNext()
			if err != nil {
				return err
			}
		}
		if err := visit(group); err != nil {
			return err
		}
	}
	return ctx.Err()
}

type relationReadRecord struct {
	value  Record
	reason string
}

func (p *Relation) records(ctx context.Context, pair *relationPair, side int) iter.Seq2[relationReadRecord, error] {
	return func(yield func(relationReadRecord, error) bool) {
		stopped := false
		columns := RelationColumns(p.query, side == 1)
		positions := func(emit func(int) bool) {
			for !stopped && ctx.Err() == nil {
				if !emit(pair.positions[side]) {
					return
				}
			}
		}
		err := p.sources[side].Read(ctx, positions, columns, func(record Record) error {
			if record.Position != pair.positions[side] {
				return errors.New("关联提取读取了错误的源位置")
			}
			projected := projectRelationRecord(record, columns)
			key, reason, err := relationKey(projected, p.query.Keys, side == 1)
			if err != nil {
				return err
			}
			if sha256.Sum256([]byte(key)) != pair.keyHash {
				return errors.New("关联提取键与预检不一致，请重新运行")
			}
			if !yield(relationReadRecord{value: projected, reason: reason}, nil) {
				stopped = true
				return errStop
			}
			return ctx.Err()
		})
		if err != nil && !errors.Is(err, errStop) {
			yield(relationReadRecord{}, err)
		}
	}
}

var errRelationPreviewFull = errors.New("relation preview complete")

func (p *Relation) stream(ctx context.Context, mode string, limit int64, visit func(RelationRow) error) (int64, error) {
	total, err := p.counts.Rows(mode)
	if err != nil {
		return 0, err
	}
	orders, err := p.borrow()
	if err != nil {
		return 0, err
	}
	defer orders[0].Close()
	defer orders[1].Close()
	if err := p.check(ctx); err != nil {
		return 0, err
	}
	read := func() (int64, error) {
		pairs := p.sortedPairs(ctx, orders, mode, total, formats.QuerySortMemoryBytes)
		if limit > 0 {
			pairs = p.previewPairs(mode)
		}
		next, stop := iter.Pull2(pairs)
		defer stop()
		var pair relationPair
		ln, ls := iter.Pull2(p.records(ctx, &pair, 0))
		defer ls()
		rn, rs := iter.Pull2(p.records(ctx, &pair, 1))
		defer rs()
		var rows int64
		lastPositions := [2]int{-1, -1}
		var retained [2]relationReadRecord
		var lastHashes [2][32]byte
		for {
			if limit > 0 && rows >= limit && rows < total {
				return rows, errRelationPreviewFull
			}
			var ok bool
			pair, err, ok = next()
			if err != nil || !ok {
				return rows, err
			}
			if err := ctx.Err(); err != nil {
				return rows, err
			}
			out := RelationRow{Sequence: rows + 1, Status: pair.status, RightMatches: pair.rightCount}
			var bytes int64
			for side, pull := range []func() (relationReadRecord, error, bool){ln, rn} {
				if pair.positions[side] < 0 {
					continue
				}
				if pair.positions[side] != lastPositions[side] {
					record, err, ok := pull()
					if err != nil {
						return rows, err
					}
					if !ok {
						return rows, errors.New("关联提取未读取完整源记录")
					}
					retained[side], lastPositions[side] = record, pair.positions[side]
					lastHashes[side] = pair.keyHash
				} else if lastHashes[side] != pair.keyHash {
					return rows, errors.New("关联同一源行出现不一致的预检键")
				}
				record := retained[side].value
				if out.Status == "invalid_key" {
					out.Reason = retained[side].reason
				}
				for _, value := range record.Values {
					bytes += int64(len(value.Raw)) + int64(len(value.Uncertain))
					if value.Display != value.Raw {
						bytes += int64(len(value.Display))
					}
				}
				if bytes > formats.RelationRecordBytes {
					return rows, errors.New("关联结果单行超过读取预算，请减少结果字段")
				}
				if side == 0 {
					out.Left = &record
				} else {
					out.Right = &record
				}
			}
			if err := visit(out); err != nil {
				return rows, err
			}
			rows++
		}
	}
	rows, err := read()
	if err != nil && !errors.Is(err, errRelationPreviewFull) {
		return rows, err
	}
	if !errors.Is(err, errRelationPreviewFull) && rows != total {
		return rows, errors.New("关联提取数量与完整预检不一致")
	}
	if checkErr := p.check(ctx); checkErr != nil {
		return rows, checkErr
	}
	return rows, err
}

// Stream rejects excessive output before invoking the sink. Sinks borrow both
// records for one callback only; publication requires this call to succeed.
func (p *Relation) Stream(ctx context.Context, mode string, maximum int64, visit func(RelationRow) error) (int64, error) {
	total, err := p.counts.Rows(mode)
	if err != nil {
		return 0, err
	}
	if maximum < 1 || maximum > formats.RelationOutputRows || visit == nil {
		return 0, errors.New("关联提取上限或输出器无效")
	}
	if total > maximum {
		return 0, fmt.Errorf("关联结果共%d条，超过本次%d条提取上限", total, maximum)
	}
	return p.stream(ctx, mode, 0, visit)
}

// Preview does not authorize the complete output. It retains only complete
// exact rows under the common row/escaped-text budget, even for a huge join.
func (p *Relation) Preview(ctx context.Context, mode string) (RelationPreview, error) {
	total, err := p.counts.Rows(mode)
	if err != nil {
		return RelationPreview{}, err
	}
	out := RelationPreview{Total: total, Rows: []RelationRow{}}
	remaining := int64(formats.RelationPreviewBytes)
	_, err = p.stream(ctx, mode, formats.RelationPreviewRows, func(row RelationRow) error {
		bytes := int64(512 + 6*len(row.Reason))
		for _, record := range []*Record{row.Left, row.Right} {
			if record == nil {
				continue
			}
			for _, value := range record.Values {
				bytes += 256 + 6*(int64(len(value.Raw))+int64(len(value.Display))+int64(len(value.Type))+int64(len(value.Uncertain)))
			}
		}
		if bytes > remaining {
			return errRelationPreviewFull
		}
		remaining -= bytes
		if row.Left != nil {
			row.Left = ownRecord(*row.Left)
		}
		if row.Right != nil {
			row.Right = ownRecord(*row.Right)
		}
		row.Reason = strings.Clone(row.Reason)
		out.Rows = append(out.Rows, row)
		return nil
	})
	if err != nil && !errors.Is(err, errRelationPreviewFull) {
		return RelationPreview{}, err
	}
	out.Truncated = int64(len(out.Rows)) < total
	return out, nil
}
