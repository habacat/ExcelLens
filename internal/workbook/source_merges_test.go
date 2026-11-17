package workbook

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOOXMLColumnMetricsUseNamedNormalInsteadOfFirstCellFont(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("xl/styles.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, err = entry.Write([]byte(`<styleSheet><fonts><font><name val="Calibri"/><sz val="9"/></font><font><name val="Consolas"/><sz val="24"/></font><font><name val="DengXian"/><sz val="11"/></font></fonts><cellStyleXfs><xf fontId="2"/></cellStyleXfs><cellXfs><xf fontId="1"/></cellXfs><cellStyles><cellStyle name="Normal" builtinId="0" xfId="0"/></cellStyles></styleSheet>`))
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	wb := &Workbook{}
	if err = wb.readStyles(archive.File[0]); err != nil {
		t.Fatal(err)
	}
	if wb.normalFont.FontName != "DengXian" || wb.normalFont.FontSize != 11 || wb.VisualStyles[0].FontName != "Consolas" {
		t.Fatal(wb.normalFont, wb.VisualStyles)
	}
}

func TestBIFFMergedCellsAfterDataAndWarmCache(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "merges.xls")
	writeBIFF8Fixture(t, file, xlsRecord(0x00E5, []byte{1, 0, 2, 0, 2, 0, 1, 0, 2, 0}))
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := wb.SourceLayout(wb.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Merges, []CellMerge{{Row: 3, Col: 2, EndRow: 3, EndCol: 3}}) {
		t.Fatal(first.Merges)
	}
	warm, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	second, err := warm.SourceLayout(warm.Sheets[0], idx)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal(second, err)
	}
	for _, bad := range [][]byte{{1}, {1, 0}, {1, 0, 2, 0, 1, 0, 1, 0, 2, 0}, {1, 0, 0, 0, 0, 0, 0, 0, 0, 1}} {
		var layout SourceLayout
		if layout.captureBIFF(0x00E5, bad) == nil {
			t.Fatal("invalid merge accepted", bad)
		}
	}
}

func TestStoredColumnWidthUsesNormalDigitMetrics(t *testing.T) {
	for _, tc := range []struct{ width, digit, want float64 }{{20, 7, 140}, {8, 7, 56}, {2187.0 / 256, 8, 68}, {5771.0 / 256, 8, 180}, {8936.0 / 256, 8, 279}, {20, 8, 160}} {
		if got := storedColumnPixels(tc.width, tc.digit); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}

func TestBIFFCountryLocalizesBuiltInDateAndExplicitFormatWins(t *testing.T) {
	data := appendBIFF(nil, 0x0809, []byte{0, 6, 5, 0})
	data = appendBIFF(data, 0x008C, []byte{86, 0, 86, 0})
	bound := make([]byte, 8)
	binary.LittleEndian.PutUint32(bound, 4)
	bound[6] = 1
	bound = append(bound, 'S')
	data = appendBIFF(data, 0x0085, bound)
	data = appendBIFF(data, 0x000A, nil)
	g, err := readXLSGlobals(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if g.formats[14] != "yyyy/m/d" {
		t.Fatal(g.formats)
	}
	wb := &Workbook{Styles: []StyleInfo{{NumFmtID: 14}}, NumFmts: g.formats}
	if got := wb.formatNumeric("45660", 0); got != "2025/1/3" {
		t.Fatal(got)
	}
	wb.NumFmts[14] = "yyyy.mm.dd"
	if got := wb.formatNumeric("45660", 0); got != "2025.01.03" {
		t.Fatal(got)
	}
}
