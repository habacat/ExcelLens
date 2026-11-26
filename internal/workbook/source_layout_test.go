package workbook

import (
	"archive/zip"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOOXMLSourceLayoutUsesIndexPassAndUpgradesOldCaches(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "layout.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, text := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="数据" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetViews><sheetView showGridLines="0"/></sheetViews><sheetFormatPr defaultRowHeight="15" defaultColWidth="10"/><cols><col min="1" max="2" width="20"/><col min="2" max="2" width="8"/></cols><sheetData><row r="1" ht="30"><c r="A1"><v>1</v></c></row><row r="2" ht="30"/><row r="4" ht="21.75"><c r="B4"><v>2</v></c></row></sheetData><mergeCells count="2"><mergeCell ref="A1:B1"/><mergeCell ref="B4:C5"/></mergeCells></worksheet>`,
	} {
		entry, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = entry.Write([]byte(text)); e != nil {
			t.Fatal(e)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := wb.SourceLayout(meta, idx)
	if err != nil {
		t.Fatal(err)
	}
	if first.GridLines == nil || *first.GridLines || first.DefaultRow != 20 || first.DefaultCol != 75 || first.Cols[1] != 140 || first.Cols[2] != 56 || len(first.Rows) != 2 || first.Rows[0] != (SourceSizeRange{Min: 1, Max: 2, Size: 40}) || first.Rows[1] != (SourceSizeRange{Min: 4, Max: 4, Size: 29}) {
		t.Fatalf("bad source dimensions %+v", first)
	}
	if !reflect.DeepEqual(first.Merges, []CellMerge{{Row: 1, Col: 1, EndRow: 1, EndCol: 2}, {Row: 4, Col: 2, EndRow: 5, EndCol: 3}}) {
		t.Fatal("OOXML merge metadata lost", first.Merges)
	}
	// The new indexing pass already captured dimensions and wrote its sidecar.
	if _, err = os.Stat(wb.sourceLayoutPath(meta)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := reopened.SourceLayout(meta, idx)
	if err != nil || !reflect.DeepEqual(first, warm) {
		t.Fatal("warm layout differs", warm, err)
	}
	// An index from an older release has no sidecar. Read opening tags only.
	if err = os.Remove(wb.sourceLayoutPath(meta)); err != nil {
		t.Fatal(err)
	}
	reopened, err = OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	upgraded, err := reopened.SourceLayout(meta, idx)
	if err != nil || !reflect.DeepEqual(first, upgraded) {
		t.Fatal("old index upgrade differs", upgraded, err)
	}
	if wb.Fingerprint != reopened.Fingerprint {
		t.Fatal("layout changed workbook identity")
	}
}

func TestBIFFSourceLayoutUsesTheSameSizeContract(t *testing.T) {
	isolateTestStorage(t)
	row := make([]byte, 16)
	binary.LittleEndian.PutUint16(row, 1)
	binary.LittleEndian.PutUint16(row[6:], 600)
	col := make([]byte, 12)
	binary.LittleEndian.PutUint16(col[4:], 20*256)
	file := filepath.Join(t.TempDir(), "layout.xls")
	writeBIFF8Fixture(t, file, xlsRecord(0x0225, []byte{0, 0, 44, 1}), xlsRecord(0x0055, []byte{10, 0}), xlsRecord(0x007D, col), xlsRecord(0x0208, row), xlsRecord(0x023E, []byte{0, 0}))
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := wb.SourceLayout(meta, idx)
	if err != nil {
		t.Fatal(err)
	}
	if l.DefaultRow != 20 || l.DefaultCol != 75 || l.Cols[1] != 140 || len(l.Rows) != 1 || l.Rows[0] != (SourceSizeRange{Min: 2, Max: 2, Size: 40}) || l.GridLines == nil || *l.GridLines {
		t.Fatalf("bad BIFF dimensions %+v", l)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := reopened.SourceLayout(meta, idx)
	if err != nil || !reflect.DeepEqual(l, warm) {
		t.Fatal(warm, err)
	}
	for id, length := range map[uint16]int{0x0225: 3, 0x0055: 1, 0x007D: 11, 0x0208: 15, 0x023E: 1} {
		var bad SourceLayout
		if bad.captureBIFF(id, make([]byte, length)) == nil {
			t.Fatal("accepted truncated layout record", id)
		}
	}
}
