package workbook

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
	"runtime"
	"testing"
	"time"

	"excellens/internal/buildinfo"
)

func TestRichSourcePresentationScale(t *testing.T) {
	if os.Getenv("EXCELLENS_RICH_SCALE") != "1" {
		t.Skip("optional 100k shared + 100k inline synthetic source observation")
	}
	root := isolateTestStorage(t)
	const count = 100_000
	file := filepath.Join(root, "rich-100k.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	part := func(name string, write func(io.Writer)) {
		t.Helper()
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		buffer := bufio.NewWriterSize(entry, 256<<10)
		write(buffer)
		if err := buffer.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	part("xl/workbook.xml", func(w io.Writer) {
		fmt.Fprint(w, `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Shared" sheetId="1" r:id="s1"/><sheet name="Inline" sheetId="2" r:id="s2"/></sheets></workbook>`)
	})
	part("xl/_rels/workbook.xml.rels", func(w io.Writer) {
		fmt.Fprint(w, `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`)
	})
	richBody := func(w io.Writer, n int) {
		fmt.Fprintf(w, `<r><rPr><b val="0"/><u val="none"/></rPr><t>中😀</t></r><r><rPr><rFont val="Arial"/><color rgb="FF1255AA"/><vertAlign val="superscript"/></rPr><t>%018d</t></r>`, n)
	}
	part("xl/sharedStrings.xml", func(w io.Writer) {
		fmt.Fprint(w, `<sst>`)
		for n := 0; n < count; n++ {
			fmt.Fprint(w, `<si>`)
			richBody(w, n)
			fmt.Fprint(w, `</si>`)
		}
		fmt.Fprint(w, `</sst>`)
	})
	for sheet := 1; sheet <= 2; sheet++ {
		part(fmt.Sprintf("xl/worksheets/sheet%d.xml", sheet), func(w io.Writer) {
			fmt.Fprint(w, `<worksheet><sheetData>`)
			for n := 0; n < count; n++ {
				if sheet == 1 {
					fmt.Fprintf(w, `<row r="%d"><c r="A%d" t="s"><v>%d</v></c></row>`, n+1, n+1, n)
				} else {
					fmt.Fprintf(w, `<row r="%d"><c r="A%d" t="inlineStr"><is>`, n+1, n+1)
					richBody(w, n)
					fmt.Fprint(w, `</is></c></row>`)
				}
			}
			fmt.Fprint(w, `</sheetData></worksheet>`)
		})
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	hash := func() string {
		r, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		h := sha256.New()
		if _, err := io.Copy(h, r); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	before := hash()
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "rowsPerSheet": count, "desktopManualAcceptance": false, "officeWpsAcceptance": false, "sourceSHA256": before}
	for _, phase := range []string{"cold", "warm"} {
		started := time.Now()
		wb, err := OpenWorkbook(file)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeRichStore(wb.richShared) })
		if wb.richShared == nil || wb.richShared.count != count || wb.RichTextNotice() != "" {
			t.Fatal("rich sidecar missing or incomplete", wb.RichTextNotice())
		}
		opened := time.Since(started)
		var indexed, streamed time.Duration
		for _, sheet := range wb.Sheets {
			wantType := "inlineStr"
			if sheet.Name == "Shared" {
				wantType = "s"
			}
			started := time.Now()
			idx, err := wb.BuildIndex(sheet, nil, nil)
			if err != nil || idx.RowCount() != count || idx.MaxRow != count || idx.MaxCol != 1 {
				t.Fatal("scale index disagreed with actual source", idx, err)
			}
			indexed += time.Since(started)
			ctx := context.Background()
			plain, err := wb.ReadRowsContext(ctx, idx, count-3, 3, 1, 1)
			if err != nil || plain[0].Cells[0].RichText != nil {
				t.Fatal("value-only read decoded presentation", err)
			}
			started = time.Now()
			seen := 0
			positions := func(yield func(int) bool) {
				for i := 0; i < idx.RowCount(); i++ {
					if !yield(i) {
						return
					}
				}
			}
			if err := wb.VisitRowsWithOptionsContext(ctx, idx, positions, 1, 1, RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true}, func(row RowData) error {
				seen++
				if len(row.Cells) != 1 {
					return fmt.Errorf("wrong cell count at %d", seen)
				}
				c := row.Cells[0]
				if row.Number != seen || c.Col != 1 || c.Ref != fmt.Sprintf("A%d", seen) || c.Type != wantType || c.Raw != fmt.Sprintf("中😀%018d", seen-1) || c.RichText == nil || c.RichText.Notice != "" || len(c.RichText.Runs) != 2 || c.RichText.Runs[0].End != 3 || c.RichText.Runs[0].Font.Bold == nil || *c.RichText.Runs[0].Font.Bold || c.RichText.Runs[1].Font.Color != "#1255AA" {
					return fmt.Errorf("source or font mismatch at %d: %#v", seen, c)
				}
				return nil
			}); err != nil || seen != count {
				t.Fatal("full presentation verification failed", seen, err)
			}
			streamed += time.Since(started)
		}
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		styleFile, err := os.Stat(wb.sharedRichPath())
		if err != nil {
			t.Fatal(err)
		}
		report[phase] = map[string]any{"openMs": opened.Milliseconds(), "indexBothSheetsMs": indexed.Milliseconds(), "verifyAll200kCellsMs": streamed.Milliseconds(), "sidecarBytes": styleFile.Size(), "heapAllocObserved": memory.HeapAlloc, "heapSysObserved": memory.HeapSys, "memoryIncludesTestFixturesAndPriorPhase": true}
	}
	if hash() != before {
		t.Fatal("scale reading changed the source")
	}
	report["sourceUnchanged"] = true
	data, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(data))
	if destination := os.Getenv("EXCELLENS_RICH_SCALE_REPORT"); destination != "" {
		if err := os.WriteFile(destination, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
