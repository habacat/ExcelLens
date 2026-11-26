package workbook

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
)

func TestConditionalStatisticsFallbackAndZeroPercentMatch(t *testing.T) {
	isolateTestStorage(t)
	var sheet strings.Builder
	sheet.WriteString("<worksheet><sheetData>")
	for row := 1; row <= formats.ConditionalStatsMinRows; row++ {
		fmt.Fprintf(&sheet, "<row r=\"%d\"><c r=\"A%d\"><v>%d</v></c></row>", row, row, row-1)
	}
	sheet.WriteString(`</sheetData><conditionalFormatting sqref="B1"><cfRule type="expression" priority="1" dxfId="0"><formula>MATCH(0%,$A$1:$A$4,0)=1</formula></cfRule></conditionalFormatting></worksheet>`)
	w, err := OpenWorkbook(sourceMetadataWorkbook(t, sheet.String(), conditionalStylesFixture))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(t *testing.T) {
		rows, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{1}, 2, 2, RowReadOptions{ConditionalFormats: true})
		if err != nil || len(rows) != 1 || len(rows[0].Cells) != 1 {
			t.Fatalf("missing matching B1: %v, %v", rows, err)
		}
		rule := rows[0].Cells[0].Conditional
		if rule == nil || rule.Matched != 1 || rule.Notice != "" {
			t.Fatalf("B1 must match the numeric zero in A1: %+v", rule)
		}
	}
	t.Run("indexed", verify)
	files, err := filepath.Glob(filepath.Join(w.CacheDir, "conditional-v1-*.db"))
	if err != nil || len(files) != 1 {
		t.Fatal("expected a completed statistics file", files, err)
	}
	// A directory in place of the derived file simulates an unavailable cache.
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(files[0], 0700); err != nil {
		t.Fatal(err)
	}
	t.Run("bounded fallback", verify)
}

// The same fixture exercises production XML, cold statistics, later viewports,
// and a reopened workbook. Set EXCELLENS_CF_SCALE_ROWS=1000000 for release evidence.
func TestConditionalStatisticsAcrossViewports(t *testing.T) {
	isolateTestStorage(t)
	n := 1000
	if raw := os.Getenv("EXCELLENS_CF_SCALE_ROWS"); raw != "" {
		var err error
		n, err = strconv.Atoi(raw)
		if err != nil || n < 100 || n > 1000000 || n%100 != 0 {
			t.Fatal("expected a multiple of 100 from 100 to 1000000")
		}
	}
	distinct := n / 100
	if raw := os.Getenv("EXCELLENS_CF_SCALE_KEYS"); raw != "" {
		var err error
		distinct, err = strconv.Atoi(raw)
		if err != nil || distinct < 1 || distinct > n/2 || n%(2*distinct) != 0 {
			t.Fatal("key count must divide rows into even-sized groups")
		}
	}
	perKey := n / distinct
	dir := t.TempDir()
	source := filepath.Join(dir, "ledger.xlsx")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	write := func(name, s string) {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = io.WriteString(w, s); e != nil {
			t.Fatal(e)
		}
	}
	write("[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/></Types>`)
	write("xl/workbook.xml", `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Ledger" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	write("xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`)
	write("xl/styles.xml", conditionalStylesFixture)
	p, err := z.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	out := bufio.NewWriterSize(p, 1<<20)
	fmt.Fprint(out, `<worksheet><sheetData>`)
	for i := 0; i < n; i++ {
		row := i + 2
		fmt.Fprintf(out, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>K%d</t></is></c><c r="B%d"><v>%d</v></c><c r="C%d"><v>2</v></c></row>`, row, row, i%distinct, row, (i/distinct)%2, row)
	}
	fmt.Fprint(out, `</sheetData>`)
	formulas := []string{
		fmt.Sprintf("COUNTIF($A:$A,$A2)=%d", perKey),
		fmt.Sprintf("SUMIF($A:$A,$A2,$C:$C)=%d", 2*perKey),
		fmt.Sprintf("COUNTIFS($A:$A,$A2,$B:$B,$B2)=%d", perKey/2),
		fmt.Sprintf("SUMIFS($C:$C,$A:$A,$A2,$B:$B,$B2)=%d", perKey),
		fmt.Sprintf("MATCH($A2,$A:$A,0)=MOD(ROW()-2,%d)+2", distinct),
		"SUM($C2:$C4)=6",
	}
	for i, formula := range formulas {
		col := string(rune('F' + i))
		fmt.Fprintf(out, `<conditionalFormatting sqref="%s2:%s%d"><cfRule type="expression" priority="%d" dxfId="0"><formula>%s</formula></cfRule></conditionalFormatting>`, col, col, n+1, i+1, formula)
	}
	fmt.Fprint(out, `</worksheet>`)
	if err = out.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	// Optional handoff of the generated synthetic source to the browser benchmark.
	if fixture := os.Getenv("EXCELLENS_CF_SCALE_FIXTURE"); fixture != "" {
		from, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		to, err := os.OpenFile(fixture, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			from.Close()
			t.Fatal(err)
		}
		_, copyErr := io.Copy(to, from)
		from.Close()
		closeErr := to.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatal(copyErr, closeErr)
		}
	}
	digest := func() string {
		f, err := os.Open(source)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	original := digest()
	open := func() (*Workbook, *SheetIndex) {
		w, e := OpenWorkbook(source)
		if e != nil {
			t.Fatal(e)
		}
		idx, e := w.BuildIndex(w.Sheets[0], nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		return w, idx
	}
	started := time.Now()
	w, idx := open()
	indexMS := time.Since(started).Milliseconds()
	read := func(at int, pages int) time.Duration {
		start := time.Now()
		for i := 0; i < pages; i++ {
			first := 2 + (at+i*71)%(n-25)
			nums := make([]int, 20)
			for j := range nums {
				nums[j] = first + j
			}
			rows, e := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, nums, 6, 11, RowReadOptions{ConditionalFormats: true})
			if e != nil {
				t.Fatal(e)
			}
			if len(rows) != len(nums) {
				t.Fatalf("requested %d rows, received %d", len(nums), len(rows))
			}
			for i, row := range rows {
				if row.Number != nums[i] {
					t.Fatalf("requested source row %d, received %d", nums[i], row.Number)
				}
				if len(row.Cells) != 6 {
					t.Fatalf("row %d: got %d formatted cells", row.Number, len(row.Cells))
				}
				for j, c := range row.Cells {
					if c.Col != j+6 || c.Conditional == nil || c.Conditional.Notice != "" || c.Conditional.Matched != 1 {
						t.Fatalf("row %d col %d: %+v", row.Number, c.Col, c.Conditional)
					}
				}
			}
		}
		return time.Since(start)
	}
	var prep time.Duration
	if n > formats.ConditionalDependencyReads {
		if !w.ConditionalStatisticsPending(context.Background(), w.Sheets[0], idx) {
			t.Fatal("cold statistics not reported")
		}
		now := time.Now()
		steps := 0
		if err := w.PrepareConditionalStatistics(context.Background(), w.Sheets[0], idx, func(done, total int) { steps++ }); err != nil || steps == 0 {
			t.Fatal("index progress", steps, err)
		}
		prep = time.Since(now)
		if w.ConditionalStatisticsPending(context.Background(), w.Sheets[0], idx) {
			t.Fatal("complete statistics still pending")
		}
	}
	cold := read(0, 1)
	files, _ := filepath.Glob(filepath.Join(w.CacheDir, "conditional-v1-*.db"))
	if len(files) != 1 {
		t.Fatal("expected one shared statistics index", files)
	}
	st, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	times := make([]float64, 16)
	for i := range times {
		times[i] = float64(read(i*(n/17), 1)) / float64(time.Millisecond)
	}
	w, idx = open()
	reopened := read(n/2, 1)
	end, err := os.Stat(files[0])
	if err != nil || end.Size() != st.Size() || !end.ModTime().Equal(st.ModTime()) {
		t.Fatal("warm read rewrote statistics index", err)
	}
	if digest() != original {
		t.Fatal("source digest changed")
	}
	slices.Sort(times)
	report := map[string]any{"result": "PASS", "rows": n, "distinctKeys": distinct, "rules": formulas, "sourceIndexMs": indexMS, "statisticsPreparationMs": float64(prep) / float64(time.Millisecond), "firstViewportMs": float64(cold) / float64(time.Millisecond), "warmViewportMedianMs": (times[7] + times[8]) / 2, "warmViewportMaxMs": times[15], "warmViewports": 16, "cellsPerViewport": 120, "reopenedViewportMs": float64(reopened) / float64(time.Millisecond), "sharedIndexBytes": st.Size(), "sharedIndexUnchangedOnWarmAndReopen": true, "sourceSHA256": original}
	raw, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(raw))
	if dst := os.Getenv("EXCELLENS_CF_SCALE_REPORT"); dst != "" {
		if e := os.WriteFile(dst, append(raw, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
