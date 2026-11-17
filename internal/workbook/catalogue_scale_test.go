package workbook

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
	"strings"
	"testing"
	"time"

	"excellens/internal/buildinfo"
)

func TestCatalogueConfirmationMillionOOXMLRecords(t *testing.T) {
	if os.Getenv("EXCELLENS_CATALOGUE_SCALE") != "1" {
		t.Skip("optional generated million-record OOXML declaration proof, not desktop acceptance")
	}
	isolateTestStorage(t)
	const records = 1_000_000
	const amount = "9007199254741015.11"
	file := filepath.Join(t.TempDir(), "million-catalogue.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	parts := catalogueSourceParts()
	parts["xl/workbook.xml"] = strings.Replace(parts["xl/workbook.xml"], "$B$2:$B$9", fmt.Sprintf("$B$2:$B$%d", records+1), 1)
	parts["xl/tables/table 1.xml"] = fmt.Sprintf(`<table xmlns="%s" id="1" name="账表" displayName="账表" ref="A1:B%d"><tableColumns count="2"><tableColumn id="1" name="编号"/><tableColumn id="2" name="金额"/></tableColumns></table>`, catalogueTestNS, records+1)
	for name, data := range parts {
		if name == "xl/worksheets/sheet1.xml" {
			continue
		}
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, data); err != nil {
			t.Fatal(err)
		}
	}
	w, err := z.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	b := bufio.NewWriterSize(w, 1<<20)
	if _, err := fmt.Fprintf(b, `<worksheet xmlns="%s" xmlns:r="%s"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>编号</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row>`, catalogueTestNS, catalogueTestRel); err != nil {
		t.Fatal(err)
	}
	for number := 1; number <= records; number++ {
		if _, err := fmt.Fprintf(b, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>%010d</t></is></c><c r="B%d" t="n"><v>%s</v></c></row>`, number+1, number+1, number, number+1, amount); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.WriteString(`</sheetData><tableParts count="1"><tablePart r:id="t1"/></tableParts></worksheet>`); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	sourceHash := func() string {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, f); err != nil {
			t.Fatal(err)
		}
		return hex.EncodeToString(hash.Sum(nil))
	}
	before := sourceHash()
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := wb.FindSheet("原表")
	started := time.Now()
	idx, err := wb.BuildIndex(meta, nil, nil)
	indexMS := time.Since(started).Milliseconds()
	if err != nil || idx.RowCount() != records+1 || idx.MaxRow != records+1 || idx.MaxCol != 2 {
		t.Fatal("real million-record index mismatch", err)
	}
	verified := 0
	positions := func(yield func(int) bool) {
		for position := 1; position < idx.RowCount(); position++ {
			if !yield(position) {
				return
			}
		}
	}
	if err := wb.VisitRowsContext(context.Background(), idx, positions, 1, 2, func(row RowData) error {
		expected := verified + 1
		if row.Number != expected+1 || len(row.Cells) != 2 || row.Cells[0].Col != 1 || row.Cells[0].Ref != fmt.Sprintf("A%d", expected+1) || row.Cells[0].Raw != fmt.Sprintf("%010d", expected) || row.Cells[0].Type != "inlineStr" || row.Cells[1].Col != 2 || row.Cells[1].Raw != amount || row.Cells[1].Type != "n" {
			return fmt.Errorf("source row %d changed: %+v", expected, row)
		}
		verified++
		return nil
	}); err != nil || verified != records {
		t.Fatal("full source verification failed", verified, err)
	}
	proofStat, err := os.Stat(wb.worksheetCataloguePath(meta))
	if err != nil || proofStat.Size() > 4096 {
		t.Fatal("declaration proof retained worksheet data", err)
	}
	assertResult := func(value Catalogue) {
		t.Helper()
		table := catalogueFind(t, value, "账表", "")
		alias := catalogueFind(t, value, "结构化", "")
		if table.Status != "ready" || !table.Table.Verified || table.Areas[0].Start != "A1" || table.Areas[0].EndRow != records+1 || alias.Status != "ready" || alias.Areas[0].Start != "B2" || alias.Areas[0].EndRow != records+1 {
			t.Fatal("million-row declaration target mismatch", table, alias)
		}
	}
	started = time.Now()
	value, err := wb.ConfirmCatalogue(context.Background(), meta, idx, nil)
	warmMS := time.Since(started).Milliseconds()
	if err != nil {
		t.Fatal(err)
	}
	assertResult(value)
	if err := os.Remove(wb.worksheetCataloguePath(meta)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	loaded := reopened.GetCachedIndex(meta.Path)
	if loaded == nil {
		t.Fatal("million-row index did not reopen")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelledAt := int64(0)
	value, err = reopened.ConfirmCatalogue(ctx, meta, loaded, func(done, _ int64, _ string) {
		if done >= 8<<20 {
			cancelledAt = done
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || cancelledAt == 0 || cancelledAt >= idx.XMLSize || len(value.Items) != 0 {
		t.Fatal("live large-scan cancellation failed", cancelledAt, idx.XMLSize, err)
	}
	if _, err := os.Stat(wb.worksheetCataloguePath(meta)); !os.IsNotExist(err) {
		t.Fatal("cancelled scan published a proof", err)
	}
	started = time.Now()
	value, err = reopened.ConfirmCatalogue(context.Background(), meta, loaded, nil)
	rebuildMS := time.Since(started).Milliseconds()
	if err != nil {
		t.Fatal(err)
	}
	assertResult(value)
	if sourceHash() != before {
		t.Fatal("source changed")
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "generatedSourceRecords": records, "verifiedSourceRecords": verified, "sourceColumns": 2, "worksheetXMLBytes": idx.XMLSize, "proofBytes": proofStat.Size(), "coldIndexMsObserved": indexMS, "warmConfirmationMsObserved": warmMS, "legacyRebuildMsObserved": rebuildMS, "activeScanCancellation": true, "cancelledAtXMLByte": cancelledAt, "sourceSHA256": before, "sourceUnchanged": true, "performanceComparison": false, "desktopManualAcceptance": false, "officeWpsAcceptance": false}
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
	if output := os.Getenv("EXCELLENS_CATALOGUE_SCALE_REPORT"); output != "" {
		if err := os.WriteFile(output, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
