package export

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/workbook"
)

func TestConditionalXLSXMillionRowSnapshot(t *testing.T) {
	if os.Getenv("EXCELLENS_CONDITIONAL_XLSX_SCALE") != "1" {
		t.Skip("optional one-million-row conditional XLSX write/reopen observation")
	}
	root := exportTestStorage(t)
	t.Cleanup(runtime.GC) // release out-of-scope mapped indexes before TempDir cleanup
	const count = 1_000_000
	source := filepath.Join(root, "million-source.xlsx")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, content := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><fonts count="1"><font><name val="Arial"/><sz val="11"/></font></fonts><cellXfs count="1"><xf fontId="0" numFmtId="0"/></cellXfs><dxfs count="1"><dxf><font><color rgb="FF1255AA"/><b val="0"/></font><fill><patternFill patternType="solid"><fgColor rgb="FFFFEE00"/></patternFill></fill></dxf></dxfs></styleSheet>`,
	} {
		if err := zipString(z, name, content); err != nil {
			t.Fatal(err)
		}
	}
	part, err := z.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	out := bufio.NewWriterSize(part, 256<<10)
	fmt.Fprint(out, `<worksheet><sheetData>`)
	for row := 1; row <= count; row++ {
		fmt.Fprintf(out, `<row r="%d"><c r="A%d"><v>9007199254740993.31</v></c><c r="B%d" t="inlineStr"><is><t>%018d</t></is></c></row>`, row, row, row, row)
	}
	fmt.Fprintf(out, `</sheetData><conditionalFormatting sqref="A1:C%d"><cfRule type="expression" priority="1" dxfId="0"><formula>TRUE()</formula></cfRule></conditionalFormatting></worksheet>`, count)
	if err := out.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	hash := func(file string) string {
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
	before := hash(source)
	started := time.Now()
	w, err := workbook.OpenWorkbook(source)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil || idx.RowCount() != count || idx.MaxRow != count || idx.MaxCol != 2 {
		t.Fatal("source extent was not exact", err)
	}
	indexTime := time.Since(started)
	selection := Selection{Sheet: w.Sheets[0], Index: idx, FirstRow: 1, LastRow: count, FirstCol: 1, LastCol: 3}
	target := filepath.Join(root, "million-frozen.xlsx")
	f, err = os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	var heapObserved uint64
	observe := func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		heapObserved = max(heapObserved, m.HeapAlloc)
	}
	observe()
	started = time.Now()
	err = Write(context.Background(), f, w, []Selection{selection}, Options{Format: "xlsx", Mode: "values", Styles: true}, func(done, total int64, _ string) {
		observe()
		if done > total || total != count {
			t.Fatal("invalid export progress", done, total)
		}
	})
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatal("scale export failed", err, closeErr)
	}
	writeTime := time.Since(started)
	started = time.Now()
	reopened, err := workbook.OpenWorkbook(target)
	if err != nil || len(reopened.Styles) != 2 {
		t.Fatal("one repeated effect did not remain one derived style", err)
	}
	readIndex, err := reopened.BuildIndex(reopened.Sheets[0], nil, nil)
	if err != nil || readIndex.RowCount() != count || readIndex.MaxRow != count || readIndex.MaxCol != 2 {
		t.Fatal("snapshot lost source rows or expanded extent for display-only blanks", err)
	}
	positions := func(yield func(int) bool) {
		for n := 0; n < count; n++ {
			if !yield(n) {
				return
			}
		}
	}
	seen := 0
	err = reopened.VisitRowsWithOptionsContext(context.Background(), readIndex, positions, 1, 3, workbook.RowReadOptions{FormatValue: true, ConditionalFormats: true}, func(row workbook.RowData) error {
		seen++
		if row.Number != seen || len(row.Cells) != 3 || row.Cells[0].Raw != "9007199254740993.31" || row.Cells[0].Type != "" || row.Cells[1].Raw != fmt.Sprintf("%018d", seen) || row.Cells[1].Type != "inlineStr" || row.Cells[2].Raw != "" {
			return fmt.Errorf("value/type/coordinate mismatch at row %d", seen)
		}
		for _, c := range row.Cells {
			v := reopened.VisualStyles[c.Style]
			if c.Style != 1 || v.Color != "#1255AA" || v.Background != "#FFEE00" || v.Bold || c.Conditional != nil {
				return fmt.Errorf("style was not frozen at %s", c.Ref)
			}
		}
		return nil
	})
	if err != nil || seen != count {
		t.Fatal("million-row snapshot verification failed", seen, err)
	}
	verifyTime := time.Since(started)
	observe()
	// Cancel after a real row has been written, not while waiting for a slot.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var progressed int64
	err = Write(ctx, io.Discard, w, []Selection{selection}, Options{Format: "xlsx", Mode: "values", Styles: true}, func(done, _ int64, _ string) {
		if done > 0 {
			progressed = done
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || progressed == 0 || progressed >= count {
		t.Fatal("active streaming export did not cancel", progressed, err)
	}
	if hash(source) != before {
		t.Fatal("scale export changed the source")
	}
	info, _ := os.Stat(target)
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "sourceRows": count, "verifiedSnapshotCells": count * 3, "sourceSHA256": before, "sourceUnchanged": true, "sourceXFCount": 1, "snapshotXFCount": len(reopened.Styles), "openAndIndexMs": indexTime.Milliseconds(), "streamExportMs": writeTime.Milliseconds(), "reopenIndexAndVerifyAllMs": verifyTime.Milliseconds(), "snapshotBytes": info.Size(), "heapAllocMaxSampled": heapObserved, "memoryIncludesSourceAndTestAndReopen": true, "cancelAfterRows": progressed, "desktopManualAcceptance": false, "officeWpsAcceptance": false}
	data, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(data))
	if destination := os.Getenv("EXCELLENS_CONDITIONAL_XLSX_SCALE_REPORT"); destination != "" {
		if err := os.WriteFile(destination, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
