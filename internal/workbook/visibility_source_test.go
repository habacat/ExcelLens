package workbook

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func TestXLSXVisibilityProjectionUsesRealValuesTypesCoordinatesAndRebuildsV4Layout(t *testing.T) {
	isolateTestStorage(t)
	x := `<worksheet><sheetPr><outlinePr summaryBelow="1" summaryRight="0"/></sheetPr><sheetFormatPr zeroHeight="1" defaultRowHeight="0" defaultColWidth="0"/><cols><col min="1" max="1" width="8" hidden="0"/><col min="2" max="2" width="0" hidden="0"/><col min="3" max="3" width="12" hidden="0"/></cols><sheetData><row r="1" hidden="0" ht="20"><c r="A1"><v>9007199254740993.31</v></c><c r="B1" t="inlineStr"><is><t>001</t></is></c></row><row r="2" hidden="1" outlineLevel="1" ht="20"><c r="A2" t="inlineStr"><is><t>000123</t></is></c></row><row r="3" hidden="1" outlineLevel="2" ht="20"><c r="A3"><f>1+1</f></c></row><row r="4" hidden="1" outlineLevel="1" collapsed="1" ht="20"/><row r="5" hidden="0" collapsed="1" ht="20"><c r="A5" t="b"><v>0</v></c></row><row r="7" hidden="0" ht="0"><c r="A7"><v>99</v></c></row><row r="100" hidden="1" outlineLevel="1"/></sheetData></worksheet>`
	file := sourceMetadataWorkbook(t, x, "")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The explicit-height blank row 4 remains an index record; metadata-only
	// tail row 100 must not extend the used data area.
	if idx.MaxRow != 7 || idx.MaxCol != 2 || idx.RowCount() != 6 {
		t.Fatal("visibility metadata changed used data extent", idx)
	}
	layout, err := w.SourceLayoutContext(context.Background(), w.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}
	view, err := layout.BuildAxisVisibility(context.Background(), "row", formats.ExcelRows, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if view.Len() != 2 || view.At(0) != 1 || view.At(1) != 5 {
		t.Fatal("source zero-size and hidden rows were confused", view.HiddenRanges())
	}
	cols, err := layout.BuildAxisVisibility(context.Background(), "col", formats.ExcelColumns, AxisViewSettings{})
	if err != nil || cols.Len() != 2 || cols.At(0) != 1 || cols.At(1) != 3 {
		t.Fatal("default zero columns were invented as positive width", err)
	}
	var positions []int
	for _, number := range []int{5, 3, 1, 7, 2} {
		position := idx.FindRowOffset(number)
		if idx.RowAt(position).Number != number {
			t.Fatal("missing source fixture row", number)
		}
		positions = append(positions, position)
	}
	order, err := NewRowOrder(context.Background(), t.TempDir(), idx.RowCount(), positions)
	if err != nil {
		t.Fatal(err)
	}
	defer order.Close()
	expanded, err := layout.BuildAxisVisibility(context.Background(), "row", formats.ExcelRows, AxisViewSettings{Groups: []AxisOutlineOverride{{AxisOutlineKey: AxisOutlineKey{Min: 2, Max: 4, Level: 1}, Collapsed: false}}})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewRowProjection(context.Background(), t.TempDir(), idx, order, expanded, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer projection.Close()
	page, err := projection.Page(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := w.ReadRowsByIndicesWithOptionsContext(context.Background(), idx, page, 1, 2, RowReadOptions{FormatValue: true, IncludeFormula: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Number != 5 || rows[0].Cells[0].Type != "b" || rows[0].Cells[0].Raw != "FALSE" || rows[1].Number != 1 || rows[1].Cells[0].Raw != "9007199254740993.31" || rows[1].Cells[1].Raw != "001" || rows[2].Number != 2 || rows[2].Cells[0].Type != "inlineStr" || rows[2].Cells[0].Raw != "000123" {
		t.Fatal("projected real source values/types/order changed", rows)
	}
	formula, err := w.ReadRowAll(idx, idx.FindRowOffset(3))
	if err != nil || !formula.Cells[0].CacheMissing || !formula.Cells[0].HasFormula || formula.Cells[0].Raw != "" {
		t.Fatal("hidden formula cache became a default value", formula, err)
	}
	if err := os.Remove(w.sourceLayoutPath(w.Sheets[0])); err != nil {
		t.Fatal(err)
	}
	legacy := sourceLayoutCache{Version: 4, Fingerprint: w.Fingerprint, Sheet: w.Sheets[0].Path, Layout: SourceLayout{}}
	legacy.Checksum = legacy.checksum()
	raw, _ := json.Marshal(legacy)
	oldPath := strings.Replace(w.sourceLayoutPath(w.Sheets[0]), "-v"+strconv.Itoa(sourceLayoutCacheVersion)+"-", "-v4-", 1)
	if err := os.WriteFile(oldPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := reopened.SourceLayoutContext(context.Background(), reopened.Sheets[0], reopened.GetCachedIndex(reopened.Sheets[0].Path))
	if err != nil || !reflect.DeepEqual(layout, warm) {
		t.Fatal("v4 cache discarded source visibility", warm, err)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(after, before) {
		t.Fatal("view projection changed source bytes")
	}
}

func biffVisibilityRow(number int, height, flags uint16) []byte {
	row := make([]byte, 16)
	binary.LittleEndian.PutUint16(row, uint16(number-1))
	binary.LittleEndian.PutUint16(row[6:], height)
	binary.LittleEndian.PutUint16(row[12:], flags)
	return xlsRecord(0x0208, row)
}
func biffVisibilityColumn(first, last int, width, flags uint16) []byte {
	col := make([]byte, 12)
	binary.LittleEndian.PutUint16(col, uint16(first-1))
	binary.LittleEndian.PutUint16(col[2:], uint16(last-1))
	binary.LittleEndian.PutUint16(col[4:], width)
	binary.LittleEndian.PutUint16(col[8:], flags)
	return xlsRecord(0x007D, col)
}

func TestBIFFVisibilitySharesAxisContractPreservesSourceAndRebuildsOldMetadataCache(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "outline.xls")
	writeBIFF8Fixture(t, file,
		xlsRecord(0x0225, []byte{2, 0, 0, 0}), xlsRecord(0x0055, []byte{0, 0}), xlsRecord(0x0081, []byte{0x40, 0}),
		biffVisibilityColumn(1, 4, 10*256, 0x0101), biffVisibilityColumn(5, 5, 8*256, 0x0800),
		biffVisibilityRow(1, 300, 0), biffVisibilityRow(2, 300, 0x21), biffVisibilityRow(3, 300, 0x22), biffVisibilityRow(4, 300, 0x31), biffVisibilityRow(5, 300, 0x10), biffVisibilityRow(6, 0, 0))
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeRows, err := w.ReadRows(idx, 0, 10, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := w.SourceLayoutContext(context.Background(), w.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}
	if !layout.ZeroHeight || !layout.DefaultRowZeroSize || !layout.DefaultColZeroSize || layout.SummaryBelow == nil || !*layout.SummaryBelow || layout.SummaryRight == nil || *layout.SummaryRight || layout.StateNotice != "" || !layout.valid() {
		t.Fatal("BIFF outline/default-size flags were not retained", layout)
	}
	if _, stale := layout.Cols[3]; stale {
		t.Fatal("later zero-width COLINFO kept an earlier nonzero width")
	}
	view, err := layout.BuildAxisVisibility(context.Background(), "row", formats.BIFFRows, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if view.Len() != 2 || view.At(0) != 1 || view.At(1) != 5 {
		t.Fatal("BIFF row visibility differs from OOXML contract", view.HiddenRanges())
	}
	groups := view.Groups()
	if len(groups) != 2 || groups[0].AxisOutlineKey != (AxisOutlineKey{Min: 2, Max: 4, Level: 1}) || !groups[0].SourceCollapsed || groups[1].Summary != 4 || !groups[1].SourceCollapsed {
		t.Fatal("BIFF outline nesting changed", groups)
	}
	if len(layout.ColStates) != 4 || layout.ColStates[1].Min != 3 || layout.ColStates[1].Level != 0 || !layout.ColStates[1].ZeroSize || !layout.ColStates[1].Hidden {
		t.Fatal("later COLINFO did not replace all axis flags", layout.ColStates)
	}
	positions := []int{2, 1, 0}
	order, err := NewRowOrder(context.Background(), t.TempDir(), idx.RowCount(), positions)
	if err != nil {
		t.Fatal(err)
	}
	defer order.Close()
	p, err := NewRowProjection(context.Background(), t.TempDir(), idx, order, view, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !slices.Equal(slices.Collect(p.Iter()), []int{0}) {
		t.Fatal("visible BIFF subset changed")
	}
	if err := os.Remove(w.sourceLayoutPath(w.Sheets[0])); err != nil {
		t.Fatal(err)
	}
	legacy := sourceLayoutCache{Version: 4, Fingerprint: w.Fingerprint, Sheet: w.Sheets[0].Path, Layout: SourceLayout{DefaultRow: 20}}
	legacy.Checksum = legacy.checksum()
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(strings.Replace(w.sourceLayoutPath(w.Sheets[0]), "-v"+strconv.Itoa(sourceLayoutCacheVersion)+"-", "-v4-", 1), raw, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := reopened.SourceLayoutContext(context.Background(), reopened.Sheets[0], reopened.GetCachedIndex(reopened.Sheets[0].Path))
	if err != nil || !reflect.DeepEqual(layout, warm) {
		t.Fatal("old BIFF layout cache hid new metadata", err)
	}
	afterRows, err := reopened.ReadRows(reopened.GetCachedIndex(reopened.Sheets[0].Path), 0, 10, 1, 2)
	if err != nil || !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatal("visibility changed BIFF value cache", err)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("BIFF metadata/projection changed source")
	}
}

func TestDefaultZeroVisibilityRetainsPositiveDimensionsBeyondGeometrySizeCap(t *testing.T) {
	isolateTestStorage(t)
	xlsx := sourceMetadataWorkbook(t, `<worksheet><sheetFormatPr defaultRowHeight="0" defaultColWidth="0"/><cols><col min="1" max="1" width="500" hidden="0"/><col min="2" max="2" hidden="0"/></cols><sheetData><row r="1" hidden="0" ht="4000"><c r="A1"><v>1</v></c></row><row r="2" hidden="0"><c r="A2"><v>2</v></c></row></sheetData></worksheet>`, "")
	biff := filepath.Join(t.TempDir(), "positive.xls")
	writeBIFF8Fixture(t, biff, xlsRecord(0x0225, []byte{0, 0, 0, 0}), xlsRecord(0x0055, []byte{0, 0}), biffVisibilityRow(1, 30000, 0), biffVisibilityColumn(1, 1, 200*256, 0))
	for _, file := range []string{xlsx, biff} {
		w, err := OpenWorkbook(file)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := w.SourceLayout(w.Sheets[0], idx)
		if err != nil {
			t.Fatal(err)
		}
		if len(layout.Rows) != 0 || len(layout.Cols) != 0 {
			t.Fatal("fixture did not exceed the existing geometry size cap", layout.Rows, layout.Cols)
		}
		rows, err := layout.BuildAxisVisibility(context.Background(), "row", MaxRowsForIndex(idx), AxisViewSettings{})
		if err != nil || rows.Len() != 1 || rows.At(0) != 1 {
			t.Fatal("positive source height was reinterpreted as zero", file, err)
		}
		cols, err := layout.BuildAxisVisibility(context.Background(), "col", formats.ExcelColumns, AxisViewSettings{})
		if err != nil || cols.Len() != 1 || cols.At(0) != 1 {
			t.Fatal("positive source width was reinterpreted as zero", file, err)
		}
	}
}
