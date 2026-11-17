package export

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/workbook"
)

type exportSequenceFixture struct {
	index *workbook.SheetIndex
	count int
	refs  []workbook.SourceRowReference
}

func (s exportSequenceFixture) Index() *workbook.SheetIndex { return s.index }
func (s exportSequenceFixture) Len() int                    { return s.count }
func (s exportSequenceFixture) Rows() iter.Seq[workbook.SourceRowReference] {
	return func(yield func(workbook.SourceRowReference) bool) {
		for _, ref := range s.refs {
			if !yield(ref) {
				return
			}
		}
	}
}

func TestExportSourceRowSequencePreservesBlanksTypesAndRejectsBrokenStreams(t *testing.T) {
	root := exportTestStorage(t)
	file := conditionalSnapshotSource(t, root, `<styleSheet><fonts count="1"><font/></fonts><fills count="1"><fill/></fills><borders count="1"><border/></borders><cellXfs count="1"><xf/></cellXfs></styleSheet>`, []string{`<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>001</t></is></c></row><row r="4"><c r="A4"><v>9007199254740993.31</v></c><c r="B4" t="b"><v>1</v></c></row></sheetData></worksheet>`}, nil)
	wb, err := workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	refs := []workbook.SourceRowReference{{Number: 4, Position: 1}, {Number: 3, Position: -1}, {Number: 1, Position: 0}}
	s := Selection{Sheet: wb.Sheets[0], Index: idx, FirstRow: 1, LastRow: 4, FirstCol: 1, LastCol: 2, Filtered: true, Compact: true, Rows: exportSequenceFixture{idx, len(refs), refs}}
	var out bytes.Buffer
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "csv", Mode: "values"}, nil); err != nil || out.String() != "9007199254740993.31,TRUE\r\n,\r\n001,\r\n" {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: "values", Styles: true}, nil); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "stream.xlsx")
	if err := os.WriteFile(output, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := workbook.OpenWorkbook(output)
	if err != nil {
		t.Fatal(err)
	}
	index, err := reopened.BuildIndex(reopened.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.ReadRows(index, 0, index.RowCount(), 1, 2)
	if err != nil || len(rows) != 2 || rows[0].Number != 1 || rows[0].Cells[0].Raw != "9007199254740993.31" || rows[0].Cells[1].Type != "b" || rows[1].Number != 3 || rows[1].Cells[0].Type != "inlineStr" || rows[1].Cells[0].Raw != "001" {
		t.Fatal(rows, err)
	}
	for _, bad := range []exportSequenceFixture{{idx, 4, refs}, {idx, 2, refs}, {idx, 1, []workbook.SourceRowReference{{Number: 1, Position: -1}}}, {idx, 1, []workbook.SourceRowReference{{Number: 1, Position: 1}}}, {idx, -1, nil}, {&workbook.SheetIndex{}, 3, refs}} {
		invalid := s
		invalid.Rows = bad
		out.Reset()
		if err := Write(context.Background(), &out, wb, []Selection{invalid}, Options{Format: "csv", Mode: "values"}, nil); err == nil {
			t.Fatal("broken source row stream accepted", bad)
		}
	}
	plain, err := wb.ReadRows(idx, 0, idx.RowCount(), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if plain[0].Cells[0].Raw != "001" {
		t.Fatal("source values changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitted := 0
	err = forEachRowWithOptions(ctx, wb, s, workbook.RowReadOptions{FormatValue: true}, func(int, int, workbook.RowData) error { emitted++; cancel(); return nil }, nil)
	if !errors.Is(err, context.Canceled) || emitted != 1 {
		t.Fatal("active row stream cancellation emitted a partial success or continued", emitted, err)
	}
}
