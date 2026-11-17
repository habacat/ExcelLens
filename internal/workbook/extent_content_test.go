package workbook

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An empty formatted column, even if its cells carry <v></v>, must not extend
// the scroll extent. A blank shared string is also empty content, not a value.
func TestContentExtentIgnoresEmptyStyledColumnsAndRows(t *testing.T) {
	content := `<worksheet><dimension ref="A1:XFD1048576"/><sheetData>` +
		`<row r="1"><c r="B1" t="inlineStr"><is><t>数据</t></is></c></row>` +
		`<row r="20"><c r="F20"><v>0</v></c></row>` +
		`<row r="999"><c r="XFD999" s="45"><v></v>
          </c><c r="XFC999" s="45" t="inlineStr"><is><t></t></is></c></row>` +
		`<row r="9999"><c r="ZZ9999" s="2" t="s"><v>0</v></c></row>` +
		`<row r="10000"><c r="ABC10000" t="s"><v>1</v></c></row>` +
		`</sheetData></worksheet>`
	rows, _, _, maxRow, maxCol, err := scanWorksheetWithShared(strings.NewReader(content), int64(len(content)), nil, nil, []string{"", "     "})
	if err != nil {
		t.Fatal(err)
	}
	if maxRow != 20 || maxCol != 6 || len(rows) != 2 {
		t.Fatalf("expected F20, got row=%d col=%d indexRows=%d", maxRow, maxCol, len(rows))
	}
}

func TestContentExtentPreservesRealSharedStringAndFormula(t *testing.T) {
	content := `<worksheet><sheetData><row r="15"><c r="Z15" s="6" t="s"><v>0</v></c></row>` +
		`<row r="37"><c r="AD37"><f t="shared" si="2"/></c></row>` +
		`<row r="57"><c r="BA57"><f></f><v></v> </c></row></sheetData></worksheet>`
	_, _, _, r, c, err := scanWorksheetWithShared(strings.NewReader(content), int64(len(content)), nil, nil, []string{"本期收入"})
	if err != nil {
		t.Fatal(err)
	}
	if r != 37 || c != 30 {
		t.Fatalf("expected AD37, got row=%d col=%d", r, c)
	}
}

func TestIndexVersionUpgradeReusesValidExistingSheetXML(t *testing.T) {
	folder := t.TempDir()
	file := filepath.Join(folder, "base.xlsx")
	writeTinyWorkbook(t, file)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := wb.Sheets[0]
	xmlPath, idxPath := wb.cachePaths(meta.Path)
	zr, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range zr.File {
		if entry.Name == meta.Path {
			rc, e := entry.Open()
			if e != nil {
				t.Fatal(e)
			}
			out, e := os.Create(xmlPath)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = out.ReadFrom(rc); e != nil {
				t.Fatal(e)
			}
			_ = out.Close()
			_ = rc.Close()
			break
		}
	}
	_ = zr.Close()
	// Simulate an index built by a previous version, while retaining the XML.
	if err := os.WriteFile(idxPath, []byte("XLIDX005"), 0600); err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 3 || idx.MaxCol != 3 {
		t.Fatalf("wrong upgraded extent: %+v", idx)
	}
	if loaded := wb.GetCachedIndex(meta.Path); loaded == nil {
		t.Fatal("new index not accessible")
	}
}
