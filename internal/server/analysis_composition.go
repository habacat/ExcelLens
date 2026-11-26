package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/composition"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func ownCompositionAnalysisSource(wb *workbook.Workbook, source *compositionSnapshot) *analysisSourceSnapshot {
	definition := source.data.plan.Definition()
	data := &analysisSourceSnapshotData{id: randomID(), source: "append:" + definition.Digest(), wb: wb, composition: source, created: time.Now()}
	return ownAnalysisSourceData(data)
}

func (a *App) borrowCompositionAnalysisSource(wb *workbook.Workbook, source, id string) (*analysisSourceSnapshot, error) {
	if id == "" {
		return nil, errors.New("联合来源尚未准备，请重新选择来源")
	}
	borrowed, err := a.borrowCompositionTask(id, wb)
	if err != nil {
		return nil, err
	}
	owner := ownCompositionAnalysisSource(wb, borrowed)
	if owner.data.source != source {
		owner.close()
		return nil, errors.New("联合来源与准备结果不一致，请重新选择")
	}
	return owner, nil
}

// Append scans and saved filter reconstruction need a shared query slot.
// Acquire it before the cache read lock; lower-level readers borrow it.
func (a *App) acquireAnalysisQuerySlot(ctx context.Context, source string, rebuildFilter bool) (func(), error) {
	if rebuildFilter || strings.HasPrefix(source, "append:") {
		return a.backgroundQuery(ctx)
	}
	return func() {}, nil
}

func compositionAnalysisDefinition(q analysis.Query, captured *analysisSourceSnapshot) (analysis.Query, error) {
	if captured != nil && captured.data.composition != nil {
		definition := captured.data.composition.data.plan.Definition()
		if q.Composition != nil {
			provided, err := q.Composition.Normalized()
			if err != nil || provided.Digest() != definition.Digest() {
				return q, errors.New("联合来源快照与查询定义不一致")
			}
		}
		q.Composition = &definition
	}
	if q.Composition != nil {
		definition, err := q.Composition.Normalized()
		if err != nil {
			return q, err
		}
		q.Composition = &definition // task owns a private, immutable descriptor
	}
	return q, analysis.ValidateSource(q)
}

func (a *App) compositionAnalysisSource(ctx context.Context, wb *workbook.Workbook, q analysis.Query, observer sourceCellObserver, borrowed *analysisSourceSnapshot) (analysisSource, error) {
	if observer != nil {
		return analysisSource{}, errors.New("联合来源的分析方案捕获尚未接入")
	}
	if borrowed == nil || borrowed.isClosed() || borrowed.data.composition == nil || borrowed.data.wb != wb || borrowed.data.source != q.Source {
		return analysisSource{}, errors.New("联合来源快照已释放，请重新运行分析")
	}
	q, err := compositionAnalysisDefinition(q, borrowed)
	if err != nil {
		return analysisSource{}, err
	}
	owner, err := borrowed.retain()
	if err != nil {
		return analysisSource{}, err
	}
	source := owner.data.composition
	if err := source.data.plan.CheckSources(ctx); err != nil {
		owner.close()
		return analysisSource{}, err
	}
	definition := q.Composition
	fields := make([]analysis.Field, len(definition.Fields))
	for i, name := range definition.Fields {
		fields[i] = analysis.Field{ID: "C" + strconv.Itoa(i+1), Name: name, Column: i + 1}
	}
	origins := make([]analysis.SourceOrigin, len(source.data.info))
	for i, info := range source.data.info {
		origins[i] = analysis.SourceOrigin{Input: i, Book: info.Ref.Book, Fingerprint: info.Ref.Fingerprint, File: info.Name, Source: info.Ref.Source, Sheet: info.Sheet, Stamp: info.Ref.Stamp, Columns: slices.Clone(definition.Inputs[i].Columns)}
	}
	scan := func(ctx context.Context, needed []string, visit func(analysis.Record) error) error {
		columns := make([]int, 0, len(needed))
		ids := make(map[int]string, len(needed))
		for _, id := range needed {
			column, err := strconv.Atoi(strings.TrimPrefix(id, "C"))
			if err != nil || id != "C"+strconv.Itoa(column) {
				continue // calculated fields are provided by the analysis engine
			}
			if column < 1 || column > len(fields) {
				return errors.New("联合来源分析字段无效")
			}
			if _, found := ids[column]; !found {
				columns = append(columns, column)
				ids[column] = id
			}
		}
		if len(columns) > analysis.MaxSourceFields {
			return errors.New("一次分析最多引用80个原始字段，请移除部分维度或筛选条件")
		}
		if len(columns) == 0 {
			columns = append(columns, 1)
			ids[1] = "C1"
		}
		err := source.scan(ctx, columns, func(row composition.Record) error {
			book := source.data.books[row.Input]
			scratch := strings.HasPrefix(origins[row.Input].Source, "scratch:")
			capability, _ := formats.Lookup(book.Format)
			values := make(map[string]analysis.Value, len(row.Cells))
			for _, mapped := range row.Cells {
				if !mapped.Exists {
					continue
				}
				cell := mapped.Data
				numeric := sourceCellNumeric(book, cell)
				if scratch {
					numeric = analysis.IsNumericLiteral(cell.Raw)
				}
				text := cell.Display
				if numeric {
					text = cell.Raw
				}
				textual := (scratch || capability.TextClustering) && !numeric && (cell.Type == "s" || cell.Type == "str" || cell.Type == "inlineStr")
				values[ids[mapped.Column]] = analysis.Value{Text: text, Display: cell.Display, Numeric: numeric, MissingCache: cell.CacheMissing, Textual: textual}
			}
			return visit(analysis.Record{Number: row.Row, Sequence: row.Sequence, Origin: &origins[row.Input], Values: values})
		})
		// Drill/sample visitors can end early. Even then every physical source
		// must still match before the caller publishes the bounded result.
		if check := source.data.plan.CheckSources(ctx); check != nil {
			return check
		}
		return err
	}
	return analysisSource{fields: fields, scan: scan, total: int(source.data.plan.Rows()), stamp: func() string { return definition.Digest() }, lease: owner}, nil
}

// Pin declarations before queueing. Resolution uses already opened or pinned
// workbooks, never a path from a saved definition.
func (a *App) pinCompositionAnalysisBooks(q analysis.Query) (func(), error) {
	books := make([]*workbook.Workbook, len(q.Composition.Inputs))
	for i, input := range q.Composition.Inputs {
		book := a.resolveReconcileBook(input.Ref.Book)
		if book == nil || book.Fingerprint != input.Ref.Fingerprint {
			return nil, fmt.Errorf("第%d份联合来源未打开或已变化，请重新选择", i+1)
		}
		books[i] = book
	}
	return a.pinWorkbooks(books...), nil
}

func compositionAnalysisPreview(ctx context.Context, src *analysisSource) ([]analysis.DrillRow, error) {
	source := src.lease.data.composition
	preview, _, err := compositionPreviewRows(ctx, source, 8)
	if err != nil {
		return nil, err
	}
	counts, numbers := make([]int, len(src.fields)), make([]int, len(src.fields))
	rows := make([]analysis.DrillRow, 0, len(preview))
	definition := source.data.plan.Definition()
	for _, row := range preview {
		cells := make([]string, min(16, len(src.fields)))
		book := source.data.books[row.Input]
		scratch := strings.HasPrefix(source.data.info[row.Input].Ref.Source, "scratch:")
		for _, cell := range row.Cells {
			if !cell.Exists {
				continue
			}
			slot := cell.Column - 1
			if strings.TrimSpace(cell.Raw) != "" {
				counts[slot]++
				data := workbook.CellData{Raw: cell.Raw, Type: cell.Type, CacheMissing: cell.CacheMissing}
				if sourceCellNumeric(book, data) || scratch && !cell.CacheMissing && analysis.IsNumericLiteral(cell.Raw) {
					numbers[slot]++
				}
			}
			if slot < len(cells) {
				cells[slot] = cell.Display
			}
		}
		info := source.data.info[row.Input]
		origin := &analysis.SourceOrigin{Input: row.Input, Book: info.Ref.Book, Fingerprint: info.Ref.Fingerprint, File: info.Name, Source: info.Ref.Source, Sheet: info.Sheet, Stamp: info.Ref.Stamp, Columns: slices.Clone(definition.Inputs[row.Input].Columns)}
		rows = append(rows, analysis.DrillRow{Row: row.Row, Cells: cells, Sequence: row.Sequence, Origin: origin})
	}
	for i := range src.fields {
		src.fields[i].Numeric = counts[i] > 0 && numbers[i]*10 >= counts[i]*7
	}
	return rows, nil
}
