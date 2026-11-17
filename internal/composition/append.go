// Package composition streams read-only, explicitly mapped table operations.
// Source lifecycle, indexing, persistence and task publication belong to the
// caller. A successful scan, including its final source checks, is required
// before a result or temporary export can be published.
package composition

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"

	"excellens/internal/formats"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

type Field struct {
	Column       int    `json:"column"`
	Name         string `json:"name"`
	HeaderHash   string `json:"headerHash,omitempty"`
	HeaderLoaded bool   `json:"headerLoaded"`
	HeaderUnique bool   `json:"headerUnique"`
	Truncated    bool   `json:"truncated,omitempty"`
	Sampled      bool   `json:"sampled,omitempty"`
	SampleType   string `json:"sampleType,omitempty"`
}

func SuggestFields(expected, actual []Field) []sourceview.HeaderMatch {
	identities := func(fields []Field) []sourceview.HeaderIdentity {
		out := make([]sourceview.HeaderIdentity, len(fields))
		for i, field := range fields {
			out[i] = sourceview.HeaderIdentity{ID: "C" + strconv.Itoa(field.Column), Hash: field.HeaderHash, Complete: field.HeaderLoaded && !field.Truncated, Unique: field.HeaderUnique}
		}
		return out
	}
	return sourceview.MatchHeaders(identities(expected), identities(actual))
}

type SourceRow struct {
	Position int // -1 denotes a genuinely absent physical row in an explicit scope
	Number   int
	Cells    []workbook.CellData
}

// Source.Scan borrows an already bound source, projects the requested columns
// and visits each included row once in source-view order. Missing physical
// cells stay absent. Empty columns visit row references without reading cell
// payloads. It must not retain or mutate the supplied column slice.
type Source struct {
	Ref            sourceview.TableRef
	Name, Sheet    string
	Format         string
	Fields         []Field
	Rows           int64
	MaxRow, MaxCol int
	Check          func(context.Context) error
	Scan           func(context.Context, []int, func(SourceRow) error) error
}

type Cell struct {
	Column       int               `json:"column"`
	SourceColumn int               `json:"sourceColumn"`
	Exists       bool              `json:"exists"`
	Data         workbook.CellData `json:"data"`
}

// Record has a unique result sequence and separate original coordinates.
// Input selects the immutable definition's source. No result row/column is
// ever assigned into the original CellData.Ref, Col, Type or cached value.
// Cells and nested presentation metadata are borrowed for the callback only.
type Record struct {
	Sequence int64  `json:"sequence"`
	Input    int    `json:"input"`
	Row      int    `json:"row"`
	Position int    `json:"position"`
	Cells    []Cell `json:"cells"`
}

type appendInput struct {
	source Source
	slots  map[int]int
}

type Append struct {
	definition sourceview.Append
	inputs     []appendInput
	rows       int64
}

func BindAppend(definition sourceview.Append, sources []Source) (*Append, error) {
	def, err := definition.Normalized()
	if err != nil {
		return nil, err
	}
	if len(sources) != len(def.Inputs) {
		return nil, errors.New("多表追加来源数量与配置不一致")
	}
	plan := &Append{definition: def, inputs: make([]appendInput, len(sources))}
	for i, source := range sources {
		ref, err := source.Ref.Normalized()
		if err != nil || ref.Digest() != def.Inputs[i].Ref.Digest() || source.Scan == nil || source.Check == nil || source.Rows < 0 || source.MaxRow < 0 || source.MaxRow > formats.ExternalRows || source.MaxCol < 1 || source.MaxCol > formats.ExcelColumns || len(source.Fields) > source.MaxCol || source.Rows > int64(source.MaxRow) || ref.HeaderRow > source.MaxRow {
			return nil, fmt.Errorf("第%d份来源的身份、范围或读取状态无效", i+1)
		}
		if capability, found := formats.Lookup(source.Format); found {
			if source.MaxRow > capability.MaxRows || source.MaxCol > capability.MaxColumns {
				return nil, fmt.Errorf("第%d份来源超过其格式容量", i+1)
			}
		} else if len(ref.Source) < 8 || ref.Source[:8] != "scratch:" || source.Format != "" {
			return nil, fmt.Errorf("第%d份来源格式无效", i+1)
		}
		fields := make(map[int]bool, len(source.Fields))
		for _, field := range source.Fields {
			if field.Column < 1 || field.Column > source.MaxCol || fields[field.Column] {
				return nil, fmt.Errorf("第%d份来源字段无效或重复", i+1)
			}
			fields[field.Column] = true
		}
		input := appendInput{source: source, slots: make(map[int]int, len(def.Fields))}
		input.source.Ref = ref
		input.source.Fields = slices.Clone(source.Fields)
		for slot, column := range def.Inputs[i].Columns {
			if column == 0 {
				continue
			}
			if !fields[column] {
				return nil, fmt.Errorf("第%d份来源的字段已变化，请重新对齐", i+1)
			}
			input.slots[column] = slot
		}
		plan.inputs[i] = input
		plan.rows += source.Rows // at most 8 * the formats row capacity
	}
	return plan, nil
}

func (a *Append) Rows() int64 { return a.rows }

func (a *Append) CheckSources(ctx context.Context) error { return a.check(ctx) }

func (a *Append) Definition() sourceview.Append {
	// BindAppend only retains a normalized definition; this cannot fail.
	definition, _ := a.definition.Normalized()
	return definition
}

// Scan uses O(output columns) working memory regardless of input row count.
// A consumer must propagate its error and publish only after Scan succeeds.
func (a *Append) Scan(ctx context.Context, columns []int, visit func(Record) error) error {
	if visit == nil || len(columns) == 0 || len(columns) > len(a.definition.Fields) {
		return errors.New("未选择有效的追加结果字段")
	}
	selected := make(map[int]int, len(columns))
	for slot, column := range columns {
		if column < 1 || column > len(a.definition.Fields) {
			return errors.New("追加结果字段列号无效")
		}
		if _, duplicate := selected[column-1]; duplicate {
			return errors.New("追加结果字段重复")
		}
		selected[column-1] = slot
	}
	if err := a.check(ctx); err != nil {
		return err
	}
	var sequence int64
	for i, input := range a.inputs {
		readColumns := make([]int, 0, len(selected))
		for sourceColumn, outputSlot := range input.slots {
			if _, found := selected[outputSlot]; found {
				readColumns = append(readColumns, sourceColumn)
			}
		}
		sort.Ints(readColumns)
		readSlots := make(map[int]int, len(readColumns))
		for slot, column := range readColumns {
			readSlots[column] = slot
		}
		seen := make([]bool, len(readColumns))
		cells := make([]Cell, len(columns))
		var count int64
		var callbackErr error
		err := input.source.Scan(ctx, readColumns, func(row SourceRow) error {
			if callbackErr != nil {
				return callbackErr
			}
			callbackErr = ctx.Err()
			if callbackErr != nil {
				return callbackErr
			}
			if count >= input.source.Rows || row.Number < 1 || row.Number > input.source.MaxRow || row.Position < -1 || row.Position >= input.source.MaxRow || row.Position == -1 && len(row.Cells) != 0 {
				callbackErr = fmt.Errorf("第%d份来源的行数或源坐标不一致", i+1)
				return callbackErr
			}
			if len(row.Cells) > len(readColumns) {
				callbackErr = fmt.Errorf("第%d份来源返回了未选择的字段", i+1)
				return callbackErr
			}
			clear(seen)
			for slot, column := range columns {
				cells[slot] = Cell{Column: column, SourceColumn: a.definition.Inputs[i].Columns[column-1]}
			}
			remaining := formats.CompositionRecordBytes - len(row.Cells)*256
			for _, cell := range row.Cells {
				readSlot, requested := readSlots[cell.Col]
				if !requested || seen[readSlot] {
					callbackErr = fmt.Errorf("第%d份来源的单元格列号无效或重复", i+1)
					return callbackErr
				}
				seen[readSlot] = true
				for _, text := range []string{cell.Ref, cell.Raw, cell.Display, cell.Formula, cell.Type, cell.ValueColor} {
					if len(text) > remaining {
						callbackErr = errors.New("追加来源单行超过读取容量，未截断数据")
						return callbackErr
					}
					remaining -= len(text)
				}
				outputSlot, mapped := input.slots[cell.Col]
				if slot, wanted := selected[outputSlot]; mapped && wanted {
					cells[slot].Exists, cells[slot].Data = true, cell
				}
			}
			sequence++
			count++
			callbackErr = visit(Record{Sequence: sequence, Input: i, Row: row.Number, Position: row.Position, Cells: cells})
			return callbackErr
		})
		if callbackErr != nil {
			return callbackErr
		}
		if err != nil {
			return err
		}
		if count != input.source.Rows {
			return fmt.Errorf("第%d份来源读取行数不完整，未发布追加结果", i+1)
		}
	}
	return a.check(ctx)
}

func (a *Append) check(ctx context.Context) error {
	for _, input := range a.inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := input.source.Check(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}
