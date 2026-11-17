package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func conditionalSnapshotSource(t *testing.T, root, styles string, sheets []string, extra map[string]string) string {
	t.Helper()
	file := filepath.Join(root, "source.xlsx")
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	var book, rels strings.Builder
	book.WriteString(`<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><workbookPr date1904="1"/><sheets>`)
	rels.WriteString(`<Relationships>`)
	for i, sheet := range sheets {
		fmt.Fprintf(&book, `<sheet name="Data%d" sheetId="%d" r:id="s%d"/>`, i+1, i+1, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="s%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		if err := zipString(z, fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), sheet); err != nil {
			t.Fatal(err)
		}
	}
	book.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)
	parts := map[string]string{"xl/workbook.xml": book.String(), "xl/_rels/workbook.xml.rels": rels.String(), "xl/styles.xml": styles}
	for k, v := range extra {
		parts[k] = v
	}
	for k, v := range parts {
		entry, err := z.CreateHeader(&zip.FileHeader{Name: k, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestConditionalXLSXSnapshotReopensSortedSparseMultiSheetValuesTypesDatesAndSourceXML(t *testing.T) {
	root := exportTestStorage(t)
	styles := `<styleSheet xmlns:x="urn:keep" x:q="x:Kind"><numFmts count="1"><numFmt numFmtId="164" formatCode="0.000"/></numFmts><fonts count="1"><font><sz val="12"/><vertAlign val="superscript"/><u val="double"/><x:keep value="x:Kind"/></font></fonts><fills count="1"><fill><patternFill patternType="none"/></fill></fills><borders count="1"><border diagonalUp="1"><top style="thin"/><diagonal style="dotted"/><x:edge value="keep"/></border></borders><cellXfs count="2"><xf numFmtId="164" fontId="0" fillId="0" borderId="0" x:q="x:Kind"><alignment indent="2" shrinkToFit="1"/><extLst><x:xf value="x:Kind"/></extLst></xf><xf numFmtId="14" fontId="0"/></cellXfs><dxfs count="2"><dxf><font><b val="0"/></font><border><right style="thin"/><top style="double"/></border><numFmt numFmtId="164" formatCode="0.00"/></dxf><dxf><fill><patternFill patternType="solid"><fgColor rgb="FFFFEE00"/></patternFill></fill></dxf></dxfs><extLst><x:sheet value="x:Kind"/></extLst></styleSheet>`
	body := `<sheetData><row r="3"><c r="B3"><v>9007199254740993.31</v></c><c r="C3" t="inlineStr"><is><t>000123</t></is></c><c r="D3" s="1"><v>1</v></c><c r="E3" t="b"><f>1=1</f><v>1</v></c><c r="F3" t="e"><f>1/0</f><v>#DIV/0!</v></c></row><row r="7"><c r="B7"><v>9007199254740993.30</v></c><c r="C7" t="inlineStr"><is><t>001</t></is></c><c r="D7" s="1"><v>2</v></c></row></sheetData>`
	rules := `<conditionalFormatting sqref="B3:B7"><cfRule type="expression" priority="1" dxfId="0"><formula>B3&gt;9007199254740993.30</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="G3:G7"><cfRule type="containsBlanks" priority="2" dxfId="1"/></conditionalFormatting>`
	const theme = `<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:themeElements><a:clrScheme name="keep"><a:accent1><a:srgbClr val="112233"/></a:accent1></a:clrScheme></a:themeElements></a:theme>`
	file := conditionalSnapshotSource(t, root, styles, []string{`<worksheet>` + body + rules + `</worksheet>`, `<worksheet>` + body + rules + `</worksheet>`}, map[string]string{"xl/theme/theme1.xml": theme})
	before, _ := os.ReadFile(file)
	w, err := workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var selected []Selection
	for _, sheet := range w.Sheets {
		idx, err := w.BuildIndex(sheet, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, Selection{Sheet: sheet, Index: idx, FirstRow: 3, LastRow: 7, FirstCol: 2, LastCol: 7})
	}
	for _, mode := range []string{"values", "display", "formula", "sorted"} {
		t.Run(mode, func(t *testing.T) {
			selections := append([]Selection(nil), selected...)
			contentMode := mode
			if mode == "sorted" {
				contentMode = "values"
				for i := range selections {
					selections[i].RowNumbers = []int{7, 5, 3} // includes a genuinely absent source row
					selections[i].Compact, selections[i].Filtered = true, true
				}
			}
			var out bytes.Buffer
			if err := Write(ctx, &out, w, selections, Options{Format: "xlsx", Mode: contentMode, Styles: true}, nil); err != nil {
				t.Fatal(err)
			}
			z, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			var exportedStyles string
			for _, entry := range z.File {
				r, err := entry.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(r)
				r.Close()
				if err != nil || validateStyleXML(string(data)) != nil {
					t.Fatal("invalid ZIP CRC or XML part", entry.Name, err)
				}
				if entry.Name == "xl/styles.xml" {
					exportedStyles = string(data)
				}
				if entry.Name == "xl/theme/theme1.xml" && string(data) != theme {
					t.Fatal("theme bytes changed")
				}
				if strings.HasPrefix(entry.Name, "xl/worksheets/") && strings.Contains(string(data), "conditionalFormatting") {
					t.Fatal("relative source rules were moved to new coordinates")
				}
			}
			for _, keep := range []string{`<x:keep value="x:Kind"/>`, `<extLst><x:xf value="x:Kind"/></extLst>`, `<x:edge value="keep"/>`, `diagonalUp="1"`, `<diagonal style="dotted"/>`, `indent="2"`, `shrinkToFit="1"`, `formatCode="0.000"`, `<extLst><x:sheet value="x:Kind"/></extLst>`} {
				if !strings.Contains(exportedStyles, keep) {
					t.Fatal("uncovered source XML was lost", keep)
				}
			}
			target := filepath.Join(t.TempDir(), "frozen.xlsx")
			if err := os.WriteFile(target, out.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			reopened, err := workbook.OpenWorkbook(target)
			if err != nil || len(reopened.Sheets) != 2 || !reopened.Date1904 || len(reopened.Styles) != 4 {
				t.Fatal("multi-sheet style dedup/date system changed", err)
			}
			for _, sheet := range reopened.Sheets {
				idx, err := reopened.BuildIndex(sheet, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := reopened.ReadRowsWithOptionsContext(ctx, idx, 0, 10, 1, 7, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, ConditionalFormats: true})
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					sourceRow := row.Number
					if mode == "sorted" {
						sourceRow = []int{7, 5, 3}[row.Number-1]
					}
					for _, c := range row.Cells {
						sourceCol := c.Col
						if mode == "sorted" {
							sourceCol++
						}
						v := reopened.VisualStyles[c.Style]
						if c.Conditional != nil {
							t.Fatal("snapshot still evaluates source rules", c)
						}
						if sourceCol == 2 && sourceRow == 3 {
							want := "9007199254740993.31"
							if c.Raw != want || c.Display != want || v.BorderRight != "0.55px solid #000000" || !strings.Contains(v.BorderTop, "double") || v.FontVertical != "superscript" || v.UnderlineStyle != "double" {
								t.Fatal("source-coordinate condition or untouched font was lost", mode, c, v)
							}
						}
						if sourceCol == 3 && sourceRow == 3 && (c.Raw != "000123" || c.Type != "inlineStr") {
							t.Fatal("native identifier changed", c)
						}
						if sourceCol == 4 && sourceRow == 3 {
							want := "1"
							if mode == "display" {
								want = "1904-01-02"
							}
							if c.Raw != want || c.Display != "1904-01-02" {
								t.Fatal("date system/source value changed", mode, c)
							}
						}
						if sourceCol == 7 && (c.Raw != "" || v.Background != "#FFEE00") {
							t.Fatal("sparse conditional blank was lost", c, v)
						}
						if sourceCol == 5 && sourceRow == 3 && mode != "display" && (c.Type != "b" || c.Raw != "TRUE" || c.HasFormula != (mode == "formula")) {
							t.Fatal("boolean formula/cache type changed", mode, c)
						}
						if sourceCol == 6 && sourceRow == 3 && mode != "display" && (c.Type != "e" || c.Raw != "#DIV/0!" || c.HasFormula != (mode == "formula")) {
							t.Fatal("error formula/cache type changed", mode, c)
						}
					}
				}
			}
		})
	}
	if after, _ := os.ReadFile(file); sha256.Sum256(after) != sha256.Sum256(before) {
		t.Fatal("snapshot changed the source file")
	}
}

func TestConditionalXLSXCaptureRejectsSameSizeSourceChangeAndAmbiguousParts(t *testing.T) {
	root := exportTestStorage(t)
	styles := `<styleSheet><fonts count="1"><font><color rgb="FFFF0000"/></font></fonts><cellXfs count="1"><xf/></cellXfs></styleSheet>`
	file := conditionalSnapshotSource(t, root, styles, []string{`<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`}, nil)
	w, err := workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(file)
	// A different same-size ZIP with its timestamp restored must not supply
	// styles for the cached values of the original workbook.
	conditionalSnapshotSource(t, root, strings.ReplaceAll(styles, "FFFF0000", "FF00FF00"), []string{`<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`}, nil)
	if changed, err := os.Stat(file); err != nil || changed.Size() != info.Size() {
		t.Fatal("fixture must exercise an actual same-size rewrite", changed, err)
	}
	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadXLSXStyleSource(context.Background(), w); err == nil {
		t.Fatal("source styles were combined with a different cached source")
	}
	file = conditionalSnapshotSource(t, root, styles, []string{`<worksheet/>`}, nil)
	w, err = workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	corrupt := bytes.Replace(data, []byte("FFFF0000"), []byte("FF00FF00"), 1)
	if bytes.Equal(data, corrupt) {
		t.Fatal("CRC fixture was not modified")
	}
	if err := os.WriteFile(file, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadXLSXStyleSource(context.Background(), w); !errors.Is(err, zip.ErrChecksum) {
		t.Fatal("stylesheet CRC failure was ignored", err)
	}
	file = conditionalSnapshotSource(t, root, styles, []string{`<worksheet/>`}, map[string]string{"xl/./styles.xml": styles})
	w, err = workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadXLSXStyleSource(context.Background(), w); err == nil || !strings.Contains(err.Error(), "重名") {
		t.Fatal("ambiguous ZIP stylesheet selected arbitrarily", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := loadXLSXStyleSource(ctx, w); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled source capture succeeded", err)
	}
}

func TestConditionalXLSXFontColorKeepsPrecedenceOverSourceAndConditionalNumberFormatColors(t *testing.T) {
	for _, tc := range []struct {
		name, sourceCode, conditionalCode string
		id                                int
	}{
		{"source custom red", `0.00;[Red](0.00);0.00`, "", 164},
		{"conditional green", "0.00", `[Green]0.000`, 164},
		{"builtin red negative", "", "", 38},
		{"quoted literal color", `0.00" [Red]"`, "", 164},
		{"hidden color-only mask", `[Red]`, "", 164},
		{"colored General keyword", `[Red]General`, "", 164},
		{"quoted General literal", `[Red]"General"`, "", 164},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := exportTestStorage(t)
			numbers, dxfNumber := "", ""
			if tc.sourceCode != "" {
				numbers = `<numFmts><numFmt numFmtId="164" formatCode="` + xmlAttr(tc.sourceCode) + `"/></numFmts>`
			}
			if tc.conditionalCode != "" {
				dxfNumber = `<numFmt numFmtId="170" formatCode="` + xmlAttr(tc.conditionalCode) + `"/>`
			}
			styles := `<styleSheet>` + numbers + `<fonts><font/></fonts><cellXfs><xf numFmtId="` + fmt.Sprint(tc.id) + `"/></cellXfs><dxfs><dxf><font><color rgb="FF1255AA"/></font>` + dxfNumber + `</dxf></dxfs></styleSheet>`
			file := conditionalSnapshotSource(t, root, styles, []string{`<worksheet><sheetData><row r="1"><c r="A1"><v>-1.25</v></c></row></sheetData><conditionalFormatting sqref="A1"><cfRule type="expression" priority="1" dxfId="0"><formula>TRUE()</formula></cfRule></conditionalFormatting></worksheet>`}, nil)
			w, err := workbook.OpenWorkbook(file)
			if err != nil {
				t.Fatal(err)
			}
			idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := w.ReadRowsWithOptionsContext(context.Background(), idx, 0, 1, 1, 1, workbook.RowReadOptions{FormatValue: true, ConditionalFormats: true})
			if err != nil {
				t.Fatal(err)
			}
			cell := rows[0].Cells[0]
			if cell.Conditional == nil || cell.Conditional.Visual.Color != "#1255AA" || cell.Conditional.ValueColor == nil || *cell.Conditional.ValueColor != "" {
				t.Fatal("source conditional font did not override number color", cell)
			}
			wantDisplay := cell.Display
			if cell.Conditional.Display != nil {
				wantDisplay = *cell.Conditional.Display
			}
			s := Selection{Sheet: w.Sheets[0], Index: idx, FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 1}
			reopened, frozen := exportReopen(t, w, s, true, filepath.Join(root, "frozen.xlsx"))
			actual := frozen[0].Cells[0]
			if actual.Raw != "-1.25" || actual.Type != "" || actual.Display != wantDisplay || actual.ValueColor != "" || reopened.VisualStyles[actual.Style].Color != "#1255AA" {
				t.Fatal("ordinary XF restored a stale number-format color", actual, wantDisplay, reopened.VisualStyles[actual.Style])
			}
			if tc.sourceCode != "" && reopened.NumFmts[164] != tc.sourceCode {
				t.Fatal("source format was modified in place")
			}
		})
	}
}
