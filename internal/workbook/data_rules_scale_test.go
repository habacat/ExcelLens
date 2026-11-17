package workbook

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/formats"
)

func TestDataRulesMillionOOXMLSourceAndLiveCancellation(t *testing.T) {
	runDataRuleMillion(t, false)
}

func TestDataRulesExtensionsMillionOOXML(t *testing.T) { runDataRuleMillion(t, true) }

func runDataRuleMillion(t *testing.T, extensions bool) {
	t.Helper()
	if os.Getenv("EXCELLENS_DATA_RULE_SCALE") != "1" {
		t.Skip("optional actual million-record OOXML rule read and source verification, not desktop acceptance")
	}
	isolateTestStorage(t)
	const records = 1_000_000
	const amount = "9007199254740993.31"
	file := filepath.Join(t.TempDir(), "million-data-rules.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, text := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
	} {
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(entry, text); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := z.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(entry, 1<<20)
	if _, err := io.WriteString(writer, `<worksheet xmlns="`+catalogueTestNS+`"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>编号</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row>`); err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= records; id++ {
		if _, err := fmt.Fprintf(writer, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>%010d</t></is></c><c r="B%d" t="n"><v>%s</v></c></row>`, id+1, id+1, id, id+1, amount); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fmt.Fprintf(writer, `</sheetData><dataValidations count="2"><dataValidation type="list" sqref="A2:A%d"><formula1>"001,002,中文"</formula1></dataValidation><dataValidation type="decimal" operator="greaterThan" sqref="B2:B%d" showErrorMessage="1"><formula1>9007199254740993.30</formula1></dataValidation></dataValidations>`, records+1, records+1); err != nil {
		t.Fatal(err)
	}
	if extensions {
		if _, err := io.WriteString(writer, dataRuleX14Fixture(fmt.Sprintf(`<k:dataValidations count="2" disablePrompts="1"><k:dataValidation type="list" showDropDown="1"><k:formula1><m:f>"001,002,中文"</m:f></k:formula1><m:sqref>A2:A%d</m:sqref></k:dataValidation><k:dataValidation type="none"><m:sqref>B2:B%d</m:sqref></k:dataValidation></k:dataValidations>`, records+1, records+1))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := io.WriteString(writer, `</worksheet>`); err != nil {
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
	hashSource := func() string {
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
	before := hashSource()
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := w.Sheets[0]
	idx, err := w.BuildIndex(meta, nil, nil)
	if err != nil || idx.RowCount() != records+1 || idx.MaxRow != records+1 || idx.MaxCol != 2 {
		t.Fatal("actual source extent differs", idx, err)
	}
	if len(w.dataRuleCache) != 0 {
		t.Fatal("ordinary index scanned data rules")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelledAt := int64(0)
	value, err := w.SourceDataRules(ctx, meta, idx, func(done, total int64, _ string) {
		if done >= 8<<20 {
			cancelledAt = done
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || value != nil || cancelledAt == 0 || cancelledAt >= idx.XMLSize || len(w.dataRuleCache) != 0 {
		t.Fatal("actual large scan cancellation published partial metadata", value, err, cancelledAt)
	}
	started := time.Now()
	value, err = w.SourceDataRules(context.Background(), meta, idx, nil)
	readMS := time.Since(started).Milliseconds()
	expectedRules := 2
	if extensions {
		expectedRules = 4
	}
	if err != nil || value.Summary().Rules != expectedRules || value.Summary().Ranges != expectedRules || len(value.nodes) != expectedRules || !value.Summary().Complete {
		t.Fatal(value, err)
	}
	for _, row := range []int{2, records / 2, records + 1} {
		for col := 1; col <= 2; col++ {
			page, err := value.At(context.Background(), row, col, 0, 50)
			if err != nil || page.Total != expectedRules/2 || page.Items[0].ID != col || !page.ScopeComplete {
				t.Fatal(page, err)
			}
			if extensions && (page.Items[1].ID != col+2 || page.Items[1].Namespace != x14DataRulesNamespace) {
				t.Fatal("x14 ranges lost source provenance", page)
			}
		}
	}
	verified := 0
	positions := func(yield func(int) bool) {
		for position := 1; position < idx.RowCount(); position++ {
			if !yield(position) {
				return
			}
		}
	}
	if err := w.VisitRowsContext(context.Background(), idx, positions, 1, 2, func(row RowData) error {
		id := verified + 1
		if row.Number != id+1 || len(row.Cells) != 2 || row.Cells[0].Ref != fmt.Sprintf("A%d", id+1) || row.Cells[0].Type != "inlineStr" || row.Cells[0].Raw != fmt.Sprintf("%010d", id) || row.Cells[1].Ref != fmt.Sprintf("B%d", id+1) || row.Cells[1].Type != "n" || row.Cells[1].Raw != amount {
			return fmt.Errorf("source row changed: %d: %+v", id, row)
		}
		verified++
		return nil
	}); err != nil || verified != records {
		t.Fatal("million source values/types/coordinates differ", verified, err)
	}
	warm, err := w.SourceDataRules(context.Background(), meta, idx, func(int64, int64, string) { t.Error("warm rules scanned source XML") })
	if err != nil || warm != value {
		t.Fatal(warm, err)
	}
	if hashSource() != before {
		t.Fatal("rules changed source file")
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "generatedSourceRecords": records, "verifiedSourceRecords": verified, "sourceColumns": 2, "worksheetXMLBytes": idx.XMLSize, "sourceRangeNodes": len(value.nodes), "retainedMetadataBudgetedBytes": value.bytes, "explicitReadMsObserved": readMS, "activeScanCancellation": true, "cancelledAtXMLByte": cancelledAt, "warmReadAvoidsXML": true, "sourceSHA256": before, "sourceUnchanged": true, "performanceComparison": false, "desktopManualAcceptance": false, "officeWpsAcceptance": false}
	report["x14Extensions"] = extensions
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
	if output := os.Getenv("EXCELLENS_DATA_RULE_SCALE_REPORT"); output != "" {
		if err := os.WriteFile(output, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDataRulesBIFFFull65536Rows(t *testing.T) {
	runDataRuleBIFFScale(t, false)
}

func TestDataRulesExtensionsBIFF65536Rows(t *testing.T) { runDataRuleBIFFScale(t, true) }

func runDataRuleBIFFScale(t *testing.T, extensions bool) {
	t.Helper()
	if os.Getenv("EXCELLENS_DATA_RULE_SCALE") != "1" {
		t.Skip("optional generated complete BIFF8 worksheet, not desktop acceptance")
	}
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "full-data-rules.xls")
	stream := appendBIFF(nil, 0x0809, []byte{0, 6, 5, 0})
	offset := len(stream) + 4
	stream = appendBIFF(stream, 0x0085, biffCatalogueSheetFixture("原表", 0))
	stream = appendBIFF(stream, 0x000A, nil)
	binary.LittleEndian.PutUint32(stream[offset:], uint32(len(stream)))
	stream = appendBIFF(stream, 0x0809, []byte{0, 6, 0x10, 0})
	for row := 0; row < formats.BIFFRows; row++ {
		label := make([]byte, 6)
		binary.LittleEndian.PutUint16(label, uint16(row))
		label = append(label, xlsU16(fmt.Sprintf("%010d", row))...)
		stream = appendBIFF(stream, 0x0204, label)
		number := make([]byte, 14)
		binary.LittleEndian.PutUint16(number, uint16(row))
		binary.LittleEndian.PutUint16(number[2:], 1)
		binary.LittleEndian.PutUint64(number[6:], math.Float64bits(float64(row)+.5))
		stream = appendBIFF(stream, 0x0203, number)
	}
	stream = append(stream, biffDataRuleHeader(1)...)
	definition := biffDataRuleRecord(3|0x80, biffDataRuleStringToken("001\x00中文"), nil, CellMerge{Row: 1, Col: 1, EndRow: formats.BIFFRows, EndCol: 1})
	var tokens []byte
	if extensions {
		tokens = []byte{0x1e, 1, 0}
		for range 2500 {
			tokens = append(tokens, 0x1e, 1, 0, 0x03)
		}
		definition = biffDataRuleRecord(7, tokens, nil, CellMerge{Row: 1, Col: 1, EndRow: formats.BIFFRows, EndCol: 1})
	}
	first := definition
	if extensions {
		first = definition[:8224]
	}
	stream = appendBIFF(stream, biffDv, first)
	if extensions {
		stream = appendBIFF(stream, 0x003C, definition[8224:])
	}
	stream = appendBIFF(stream, 0x000A, nil)
	writeBIFFCFBFixture(t, file, stream)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := sha256.Sum256(before)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := w.Sheets[0]
	idx, err := w.BuildIndex(meta, nil, nil)
	if err != nil || idx.RowCount() != formats.BIFFRows || idx.MaxRow != formats.BIFFRows {
		t.Fatal(idx, err)
	}
	index, err := w.SourceDataRules(context.Background(), meta, idx, nil)
	if err != nil || index.Summary().Rules != 1 || len(index.nodes) != 1 {
		t.Fatal(index, err)
	}
	page, err := index.At(context.Background(), formats.BIFFRows, 1, 0, 50)
	if err != nil || page.Total != 1 || page.Items[0].RecordHex != hex.EncodeToString(first) || !page.ScopeComplete {
		t.Fatal(page, err)
	}
	if extensions && (len(page.Items[0].ContinuationHex) != 1 || page.Items[0].Formula1Tokens != hex.EncodeToString(tokens) || page.Items[0].Status != "unresolved") {
		t.Fatal("large continued rule lost source tokens or unknown state", page)
	}
	verified := 0
	positions := func(yield func(int) bool) {
		for at := 0; at < idx.RowCount(); at++ {
			if !yield(at) {
				return
			}
		}
	}
	if err := w.VisitRowsContext(context.Background(), idx, positions, 1, 2, func(row RowData) error {
		id := verified
		if row.Number != id+1 || len(row.Cells) != 2 || row.Cells[0].Ref != fmt.Sprintf("A%d", id+1) || row.Cells[0].Type != "inlineStr" || row.Cells[0].Raw != fmt.Sprintf("%010d", id) || row.Cells[1].Ref != fmt.Sprintf("B%d", id+1) || row.Cells[1].Type != "n" || row.Cells[1].Raw != strconv.Itoa(id)+".5" {
			return fmt.Errorf("source BIFF row changed: %d: %+v", id, row)
		}
		verified++
		return nil
	}); err != nil || verified != formats.BIFFRows {
		t.Fatal(verified, err)
	}
	after, err := os.ReadFile(file)
	if err != nil || sha256.Sum256(after) != beforeHash {
		t.Fatal("BIFF source changed", err)
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "generatedSourceRecords": formats.BIFFRows, "verifiedSourceRecords": verified, "sourceColumns": 2, "sourceBytes": len(before), "sourceRangeNodes": len(index.nodes), "sourceSHA256": hex.EncodeToString(beforeHash[:]), "sourceUnchanged": true, "performanceComparison": false, "desktopManualAcceptance": false, "officeWpsAcceptance": false}
	report["continuedRule"] = extensions
	report["biffRowReaderVersion"] = biffRowReaderVersion
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
	if output := os.Getenv("EXCELLENS_DATA_RULE_BIFF_REPORT"); output != "" {
		if err := os.WriteFile(output, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
