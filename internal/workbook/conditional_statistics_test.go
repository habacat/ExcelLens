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
	sheet.WriteString(`</sheetData><conditionalFormatting sqref="B1"><cfRule type="expression" priority="1" dxfId="0"><formula>MATCH(0%,$A$1:$A$32,0)=1</formula></cfRule></conditionalFormatting></worksheet>`)
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
	w.SetConditionalStatsPolicy(func() (int64, func(string) bool) { return 1, nil })
	t.Run("configured budget applies to synchronous reads", verify)
	if files, err := filepath.Glob(filepath.Join(ConditionalStatsRoot(), "*.db")); err != nil || len(files) != 0 {
		t.Fatal("synchronous read published outside the configured budget", files, err)
	}
	w.SetConditionalStatsPolicy(nil)
	index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}
	index.clearStatisticsIssue()
	t.Run("indexed", verify)
	files, err := filepath.Glob(filepath.Join(ConditionalStatsRoot(), ConditionalStatsFilePrefix(w.Fingerprint)+"*.db"))
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
	files, _ := filepath.Glob(filepath.Join(ConditionalStatsRoot(), ConditionalStatsFilePrefix(w.Fingerprint)+"*.db"))
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
	reopenStart := time.Now()
	w, idx = open()
	reopenIndex := time.Since(reopenStart)
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
	report["reopenAndIndexMs"] = float64(reopenIndex) / float64(time.Millisecond)
	report["warmViewportSamplesMs"] = times
	raw, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(raw))
	if dst := os.Getenv("EXCELLENS_CF_SCALE_REPORT"); dst != "" {
		if e := os.WriteFile(dst, append(raw, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}

func TestShortAndSlidingSumsAvoidDiskStatisticsAndStayExact(t *testing.T) {
	isolateTestStorage(t)
	var sheet strings.Builder
	sheet.WriteString("<worksheet><sheetData>")
	for row := 2; row <= 120; row++ {
		fmt.Fprintf(&sheet, `<row r="%d"><c r="C%d"><v>2</v></c></row>`, row, row)
	}
	// Short 3-row moving sum -> StrategyViewportCache; 32-row moving sum -> StrategySlidingWindow.
	sheet.WriteString(`</sheetData>`)
	sheet.WriteString(`<conditionalFormatting sqref="F2:F120"><cfRule type="expression" priority="1" dxfId="0"><formula>SUM($C2:$C4)=6</formula></cfRule></conditionalFormatting>`)
	sheet.WriteString(`<conditionalFormatting sqref="G2:G120"><cfRule type="expression" priority="2" dxfId="1"><formula>SUM($C2:$C33)=64</formula></cfRule></conditionalFormatting>`)
	sheet.WriteString(`</worksheet>`)
	w, err := OpenWorkbook(sourceMetadataWorkbook(t, sheet.String(), conditionalStylesFixture))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.ConditionalStatisticsState(context.Background(), w.Sheets[0], idx)
	if err != nil || info.State != ConditionalStatsNotNeeded || info.Plans != 0 {
		t.Fatalf("short and sliding sums must not build disk statistics: %+v, err=%v", info, err)
	}
	rows, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{2, 10, 50, 88}, 6, 7, RowReadOptions{ConditionalFormats: true, ConditionalStatsReadOnly: true})
	if err != nil || len(rows) != 4 {
		t.Fatalf("read failed: %v, len=%d", err, len(rows))
	}
	for _, row := range rows {
		if len(row.Cells) != 2 {
			t.Fatalf("row %d: expected 2 matched cells, got %+v", row.Number, row.Cells)
		}
		for _, cell := range row.Cells {
			if cell.Conditional == nil || cell.Conditional.Matched != 1 || cell.Conditional.Pending || cell.Conditional.Notice != "" {
				t.Fatalf("row %d col %d: unexpected conditional %+v", row.Number, cell.Col, cell.Conditional)
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(ConditionalStatsRoot(), "*.db"))
	if len(files) != 0 {
		t.Fatalf("unexpected disk statistics files created: %v", files)
	}
}

func TestReadOnlyForegroundNeverBlocksOnMissingOrDamagedStatistics(t *testing.T) {
	isolateTestStorage(t)
	var sheet strings.Builder
	sheet.WriteString("<worksheet><sheetData>")
	for row := 2; row <= 64; row++ {
		fmt.Fprintf(&sheet, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>K%d</t></is></c></row>`, row, row, row%4)
	}
	// Priority 1 uses whole-column COUNTIF (disk group). Priority 2 is always TRUE and must NOT leak through while pending!
	sheet.WriteString(`</sheetData><conditionalFormatting sqref="F2:F64"><cfRule type="expression" priority="1" dxfId="0"><formula>COUNTIF($A:$A,$A2)&gt;1</formula></cfRule><cfRule type="expression" priority="2" dxfId="1"><formula>TRUE()</formula></cfRule></conditionalFormatting></worksheet>`)
	w, err := OpenWorkbook(sourceMetadataWorkbook(t, sheet.String(), conditionalStylesFixture))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.ConditionalStatisticsState(context.Background(), w.Sheets[0], idx)
	if err != nil || info.State != ConditionalStatsMissing {
		t.Fatalf("expected missing state, got %+v, %v", info, err)
	}
	// Read-only foreground read must NOT create a disk file and must mark cell pending without forging priority-2 style.
	rows, err := w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{2}, 6, 6, RowReadOptions{ConditionalFormats: true, ConditionalStatsReadOnly: true, ConditionalStatsStatus: "running"})
	if err != nil || len(rows) != 1 || len(rows[0].Cells) != 1 {
		t.Fatalf("read failed: %v, %+v", err, rows)
	}
	cf := rows[0].Cells[0].Conditional
	if cf == nil || !cf.Pending || cf.Matched != 0 || cf.Visual != nil || !strings.Contains(cf.Notice, "计算中") {
		t.Fatalf("expected honest pending state without forged style, got %+v", cf)
	}
	if _, err := os.Stat(info.FilePath); !os.IsNotExist(err) {
		t.Fatalf("read-only foreground read created disk index: %v", err)
	}

	// Build in background -> becomes ready.
	if err := w.PrepareConditionalStatistics(context.Background(), w.Sheets[0], idx, nil); err != nil {
		t.Fatal(err)
	}
	info, err = w.ConditionalStatisticsState(context.Background(), w.Sheets[0], idx)
	if err != nil || info.State != ConditionalStatsReady {
		t.Fatalf("expected ready state, got %+v, %v", info, err)
	}

	// Truncate to zero bytes -> Check detects damaged, foreground read returns rebuilding pending without rebuilding synchronously!
	if err := os.WriteFile(info.FilePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	info, err = w.ConditionalStatisticsState(context.Background(), w.Sheets[0], idx)
	if err != nil || info.State != ConditionalStatsDamaged {
		t.Fatalf("zero-byte cache must report damaged, got %+v, %v", info, err)
	}
	rows, err = w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{2}, 6, 6, RowReadOptions{ConditionalFormats: true, ConditionalStatsReadOnly: true})
	if err != nil || len(rows) != 1 || len(rows[0].Cells) != 1 {
		t.Fatalf("read failed on damaged cache: %v, %+v", err, rows)
	}
	cf = rows[0].Cells[0].Conditional
	if cf == nil || !cf.Pending || cf.Matched != 0 || !strings.Contains(cf.Notice, "重建中") {
		t.Fatalf("expected rebuilding pending state on damaged file, got %+v", cf)
	}
	if st, err := os.Stat(info.FilePath); err != nil || st.Size() != 0 {
		t.Fatalf("foreground read must not synchronously rebuild damaged file: size=%v err=%v", st, err)
	}

	// Background rebuild repairs the zero-byte file; subsequent read-only foreground read succeeds with real style!
	if err := w.PrepareConditionalStatistics(context.Background(), w.Sheets[0], idx, nil); err != nil {
		t.Fatal(err)
	}
	rows, err = w.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{2}, 6, 6, RowReadOptions{ConditionalFormats: true, ConditionalStatsReadOnly: true})
	if err != nil || len(rows) != 1 || len(rows[0].Cells) != 1 {
		t.Fatalf("read failed after rebuild: %v, %+v", err, rows)
	}
	cf = rows[0].Cells[0].Conditional
	if cf == nil || cf.Pending || cf.Matched != 2 || cf.Notice != "" {
		t.Fatalf("expected full matched rules after rebuild, got %+v", cf)
	}
}
