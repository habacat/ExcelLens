package composition

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"excellens/internal/formats"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

func appendFixture() (sourceview.Append, []Source) {
	definition := sourceview.Append{Version: sourceview.AppendVersion, Name: "追加", Fields: []string{"编号", "金额", "备注"}, Inputs: []sourceview.AppendInput{
		{Ref: sourceview.TableRef{Book: "path-a", Fingerprint: "same", Source: "wb:First", Stamp: "a", HeaderRow: 1}, Columns: []int{1, 2, 0}},
		{Ref: sourceview.TableRef{Book: "path-b", Fingerprint: "same", Source: "wb:Second", Stamp: "b", HeaderRow: 1}, Columns: []int{4, 0, 2}},
	}}
	rows := [][]SourceRow{
		{{Position: 1, Number: 3, Cells: []workbook.CellData{{Ref: "A3", Col: 1, Raw: "0001", Display: "0001", Type: "s"}, {Ref: "B3", Col: 2, Raw: "9007199254740993.01", Display: "9,007,199,254,740,993.01", Type: "n", Style: 7}}},
			{Position: 2, Number: 9, Cells: []workbook.CellData{{Ref: "A9", Col: 1, Type: "inlineStr"}, {Ref: "B9", Col: 2, Formula: "", HasFormula: true, SharedFormula: true, CacheMissing: true}}}},
		{{Position: 1, Number: 2, Cells: []workbook.CellData{{Ref: "B2", Col: 2, Raw: "=source text", Display: "=source text", Type: "s"}, {Ref: "D2", Col: 4, Raw: "123.00", Display: "123.00", Type: "s"}}},
			{Position: 2, Number: 20, Cells: []workbook.CellData{{Ref: "B20", Col: 2, Raw: "#VALUE!", Display: "#VALUE!", Type: "e", HasFormula: true, Formula: "1+\"x\""}}}},
	}
	sources := make([]Source, len(rows))
	for i := range sources {
		sources[i] = Source{Ref: definition.Inputs[i].Ref, Name: "same.xlsx", Sheet: "sheet", Format: "xlsx", Rows: int64(len(rows[i])), MaxRow: 20, MaxCol: 4,
			Fields: []Field{{Column: 1}, {Column: 2}, {Column: 3}, {Column: 4}}, Check: func(context.Context) error { return nil },
			Scan: func(ctx context.Context, columns []int, visit func(SourceRow) error) error {
				for _, row := range rows[i] {
					var cells []workbook.CellData
					for _, cell := range row.Cells {
						if slices.Contains(columns, cell.Col) {
							cells = append(cells, cell)
						}
					}
					row.Cells = cells
					if err := visit(row); err != nil {
						return err
					}
				}
				return ctx.Err()
			}}
	}
	return definition, sources
}

func TestAppendPreservesEveryRawTypeFormulaAndSourceCoordinate(t *testing.T) {
	definition, sources := appendFixture()
	plan, err := BindAppend(definition, sources)
	if err != nil || plan.Rows() != 4 {
		t.Fatal(plan, err)
	}
	definition.Inputs[0].Columns[0] = 4
	sources[0].Fields[0].Column = 4
	copy := plan.Definition()
	copy.Inputs[0].Columns[0] = 3
	if plan.Definition().Inputs[0].Columns[0] != 1 {
		t.Fatal("plan retained mutable caller definitions")
	}
	var got []Record
	err = plan.Scan(context.Background(), []int{1, 2, 3}, func(row Record) error {
		row.Cells = slices.Clone(row.Cells)
		got = append(got, row)
		return nil
	})
	if err != nil || len(got) != 4 {
		t.Fatal(got, err)
	}
	if got[0].Sequence != 1 || got[0].Input != 0 || got[0].Row != 3 || got[1].Row != 9 || got[2].Sequence != 3 || got[2].Input != 1 || got[3].Row != 20 {
		t.Fatal("result positions replaced sparse source coordinates", got)
	}
	if cell := got[0].Cells[0]; cell.SourceColumn != 1 || cell.Data.Type != "s" || cell.Data.Raw != "0001" || cell.Data.Ref != "A3" {
		t.Fatal("identifier changed", cell)
	}
	if cell := got[0].Cells[1]; cell.Data.Raw != "9007199254740993.01" || cell.Data.Display != "9,007,199,254,740,993.01" || cell.Data.Type != "n" || cell.Data.Style != 7 {
		t.Fatal("numeric precision or source presentation changed", cell)
	}
	if cell := got[2].Cells[0]; cell.Column != 1 || cell.SourceColumn != 4 || cell.Data.Col != 4 || cell.Data.Ref != "D2" || cell.Data.Type != "s" || cell.Data.Raw != "123.00" {
		t.Fatal("mapping changed source text amount eligibility or coordinates", cell)
	}
	if got[0].Cells[2].SourceColumn != 0 || got[0].Cells[2].Exists || got[3].Cells[0].SourceColumn != 4 || got[3].Cells[0].Exists || !got[1].Cells[0].Exists || got[1].Cells[0].Data.Type != "inlineStr" {
		t.Fatal("missing binding, absent physical cell and explicit blank were conflated", got)
	}
	if cell := got[1].Cells[1].Data; !cell.CacheMissing || !cell.HasFormula || !cell.SharedFormula || cell.Formula != "" {
		t.Fatal("unavailable source formula or cache was fabricated", cell)
	}
	if cell := got[3].Cells[2].Data; cell.Type != "e" || !cell.HasFormula || cell.Formula != "1+\"x\"" {
		t.Fatal("formula error state lost", cell)
	}
}

func TestAppendProjectionUsesOnlyNeededColumnsAndWalksMissingRowsWithoutPayload(t *testing.T) {
	definition, sources := appendFixture()
	var columns [][]int
	for i := range sources {
		scan := sources[i].Scan
		sources[i].Scan = func(ctx context.Context, cols []int, visit func(SourceRow) error) error {
			columns = append(columns, slices.Clone(cols))
			return scan(ctx, cols, visit)
		}
	}
	plan, _ := BindAppend(definition, sources)
	var count int
	err := plan.Scan(context.Background(), []int{3}, func(row Record) error {
		count++
		if len(row.Cells) != 1 || row.Cells[0].Column != 3 || row.Input == 0 && row.Cells[0].Exists {
			t.Fatal("projection widened", row)
		}
		return nil
	})
	if err != nil || count != 4 || len(columns) != 2 || len(columns[0]) != 0 || !slices.Equal(columns[1], []int{2}) {
		t.Fatal("missing projection read an arbitrary source column or lost rows", columns, count, err)
	}
}

func TestAppendRejectsSourceChangesTruncationReaderErrorsAndCancellation(t *testing.T) {
	changed := errors.New("source changed")
	consumer := errors.New("consumer failed")
	for name, configure := range map[string]func([]Source){
		"changed before scan": func(s []Source) { s[1].Check = func(context.Context) error { return changed } },
		"changed after scan": func(s []Source) {
			checks := 0
			s[0].Check = func(context.Context) error {
				checks++
				if checks > 1 {
					return changed
				}
				return nil
			}
		},
		"truncated": func(s []Source) { s[1].Rows++ },
		"extra row": func(s []Source) { s[0].Rows-- },
		"reader failed": func(s []Source) {
			s[1].Scan = func(context.Context, []int, func(SourceRow) error) error { return changed }
		},
		"swallowed callback": func(s []Source) {
			s[0].Scan = func(_ context.Context, _ []int, visit func(SourceRow) error) error {
				_ = visit(SourceRow{Position: -2, Number: 2})
				_ = visit(SourceRow{Position: 1, Number: 3})
				return nil
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			definition, sources := appendFixture()
			configure(sources)
			plan, err := BindAppend(definition, sources)
			if err != nil {
				t.Fatal(err)
			}
			if err := plan.Scan(context.Background(), []int{1}, func(Record) error { return nil }); err == nil {
				t.Fatal("failed or partial source published as a complete append")
			}
		})
	}
	definition, sources := appendFixture()
	plan, _ := BindAppend(definition, sources)
	if err := plan.Scan(context.Background(), []int{1}, func(Record) error { return consumer }); !errors.Is(err, consumer) {
		t.Fatal("consumer error swallowed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	count := 0
	err := plan.Scan(ctx, []int{1}, func(Record) error { count++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || count != 1 {
		t.Fatal("cancelled scan kept emitting rows", count, err)
	}
}

func TestAppendRejectsWrongBindingsInvalidCellsAndOversizedRecords(t *testing.T) {
	for name, configure := range map[string]func([]Source){
		"foreign path":    func(s []Source) { s[0].Ref.Book = "other" },
		"foreign stamp":   func(s []Source) { s[0].Ref.Stamp = "other" },
		"missing check":   func(s []Source) { s[0].Check = nil },
		"missing scan":    func(s []Source) { s[0].Scan = nil },
		"missing field":   func(s []Source) { s[0].Fields = s[0].Fields[2:] },
		"false format":    func(s []Source) { s[0].Format = "unsupported" },
		"format capacity": func(s []Source) { s[0].MaxRow = formats.ExternalRows },
		"negative total":  func(s []Source) { s[0].Rows = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			definition, sources := appendFixture()
			configure(sources)
			if _, err := BindAppend(definition, sources); err == nil {
				t.Fatal("invalid source bound")
			}
		})
	}
	for _, cells := range [][]workbook.CellData{
		{{Col: 1}, {Col: 1}},
		{{Col: 3}},
		{{Col: 1, Raw: strings.Repeat("9", formats.CompositionRecordBytes+1)}},
	} {
		definition, sources := appendFixture()
		sources[0].Scan = func(_ context.Context, _ []int, visit func(SourceRow) error) error {
			return visit(SourceRow{Position: 1, Number: 2, Cells: cells})
		}
		plan, _ := BindAppend(definition, sources)
		if err := plan.Scan(context.Background(), []int{1, 2}, func(Record) error { return nil }); err == nil {
			t.Fatal("invalid or oversized source row accepted")
		}
	}
	definition, sources := appendFixture()
	plan, _ := BindAppend(definition, sources)
	for _, cols := range [][]int{nil, {0}, {4}, {1, 1}} {
		if err := plan.Scan(context.Background(), cols, func(Record) error { return nil }); err == nil {
			t.Fatal("invalid output projection accepted", cols)
		}
	}
	definition, sources = appendFixture()
	definition.Inputs[0].Ref.Source, definition.Inputs[0].Ref.HeaderRow = "scratch:one", 0
	sources[0].Ref, sources[0].Format = definition.Inputs[0].Ref, ""
	sources[0].MaxRow = formats.ExternalRows + 1
	if _, err := BindAppend(definition, sources); err == nil {
		t.Fatal("analysis source bypassed the shared coordinate capacity")
	}
}
