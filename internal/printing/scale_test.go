package printing

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/workbook"
)

func TestPaginationMillionRowsFitAndSourceParity(t *testing.T) {
	if os.Getenv("EXCELLENS_PAGINATION_SCALE") != "1" {
		t.Skip("optional actual million-row source-bound pagination")
	}
	root := t.TempDir()
	for _, key := range []string{"LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(key, root)
	}
	const records = 1_000_000
	file := filepath.Join(root, "million-pagination.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for path, text := range map[string]string{"xl/workbook.xml": `<workbook xmlns="` + testNS + `" xmlns:r="` + testRel + `"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets><definedNames><definedName name="_xlnm.Print_Area" localSheetId="0">'Data'!$A$1:$B$1000000</definedName><definedName name="_xlnm.Print_Titles" localSheetId="0">'Data'!$1:$1</definedName></definedNames></workbook>`, "xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="` + testRel + `/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`} {
		entry, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, text); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := z.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(entry, 1<<20)
	if _, err := io.WriteString(writer, `<worksheet xmlns="`+testNS+`"><sheetFormatPr defaultRowHeight="10" baseColWidth="8"/><cols><col min="1" max="2" width="10"/></cols><sheetData>`); err != nil {
		t.Fatal(err)
	}
	var expected int64
	for row := 1; row <= records; row++ {
		height := 10
		if row%2 == 0 {
			height = 15
		}
		hidden := ""
		if row%7 == 0 {
			hidden = ` hidden="1"`
		} else if row > 1 {
			expected += int64(height) * 1_000_000_000
		}
		if _, err := fmt.Fprintf(writer, `<row r="%d" ht="%d"%s><c r="A%d" t="inlineStr"><is><t>%010d</t></is></c><c r="B%d" t="n"><v>9007199254740993.31</v></c></row>`, row, height, hidden, row, row, row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := io.WriteString(writer, `</sheetData><rowBreaks count="1" manualBreakCount="1"><brk id="500000" man="1"/></rowBreaks></worksheet>`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	hash := func() string {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	before := hash()
	w, err := workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil || idx.RowCount() != records {
		t.Fatal(err)
	}
	source, err := w.ReadPrintSource(context.Background(), w.Sheets[0], idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := options("500pt", "700pt")
	o.ScalePercent = ""
	o.Fit = &Fit{1, 1000}
	started := time.Now()
	plan, err := New(context.Background(), source, o)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary().Pages > 1000 || plan.Summary().Pages < 1 {
		t.Fatal(plan.Summary())
	}
	var bodyUnits int64
	last := 1
	parts := 0
	for at := 0; at < plan.Summary().Pages; at++ {
		page, err := plan.Page(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		if page.Content.Width > 500_000_000_000 || page.Content.Height > 700_000_000_000 {
			t.Fatal("fit overflow", page)
		}
		for _, part := range page.Rows {
			parts++
			if part.Repeated {
				if part.First != 1 || part.Last != 1 {
					t.Fatal(part)
				}
				continue
			}
			if part.First != last+1 {
				t.Fatal("source body row gap/duplication", last, part)
			}
			last = part.Last
			bodyUnits += part.Length
		}
	}
	if bodyUnits != expected || last != records {
		t.Fatal("source geometry missing", bodyUnits, expected, last)
	}
	verified := 0
	positions := func(yield func(int) bool) {
		for row := 0; row < records; row++ {
			if !yield(row) {
				return
			}
		}
	}
	if err := w.VisitRowsContext(context.Background(), idx, positions, 1, 2, func(row workbook.RowData) error {
		verified++
		if row.Number != verified || len(row.Cells) != 2 || row.Cells[0].Raw != fmt.Sprintf("%010d", verified) || row.Cells[0].Type != "inlineStr" || row.Cells[0].Ref != "A"+fmt.Sprint(verified) || row.Cells[1].Raw != "9007199254740993.31" || row.Cells[1].Type != "n" || row.Cells[1].Ref != "B"+fmt.Sprint(verified) {
			return fmt.Errorf("source row changed: %d", verified)
		}
		return nil
	}); err != nil || verified != records {
		t.Fatal(verified, err)
	}
	if hash() != before {
		t.Fatal("source file changed")
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "generatedSourceRecords": records, "verifiedSourceRecords": verified, "pages": plan.Summary().Pages, "pageAxisParts": parts, "sourceBodyUnits": fmt.Sprint(bodyUnits), "paginationMsObserved": elapsed, "sourceUnchanged": true, "sourceSHA256": before, "sourceIdentity": source.Identity(), "sourceGeometrySegments": source.Geometry().Summary().RowSegments, "fitWidth": 1, "fitHeight": 1000, "desktopManualAcceptance": false, "officeWpsAcceptance": false, "performanceComparison": false}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if path := os.Getenv("EXCELLENS_PAGINATION_SCALE_REPORT"); path != "" {
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
