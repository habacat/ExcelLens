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

func TestPrintSettingsMillionOOXMLSourceAndActiveCancellation(t *testing.T) {
	if os.Getenv("EXCELLENS_PRINT_SCALE") != "1" {
		t.Skip("optional actual million-row print metadata/source check")
	}
	isolateTestStorage(t)
	const records = 1_000_000
	file := filepath.Join(t.TempDir(), "million-print.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, text := range printTestParts("", `<definedName name="_xlnm.Print_Area" localSheetId="0">'Data'!$A$1:$B$1000000</definedName>`) {
		if text == "" {
			continue
		}
		entry, err := z.Create(name)
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
	if _, err := io.WriteString(writer, `<worksheet xmlns="`+catalogueTestNS+`"><sheetData>`); err != nil {
		t.Fatal(err)
	}
	for row := 1; row <= records; row++ {
		if _, err := fmt.Fprintf(writer, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>%010d</t></is></c><c r="B%d" t="n"><v>9007199254740993.31</v></c></row>`, row, row, row, row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := io.WriteString(writer, `</sheetData><pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/><pageSetup paperSize="9" orientation="landscape"/><headerFooter><oddHeader>&amp;L原表&amp;P</oddHeader></headerFooter><rowBreaks count="1" manualBreakCount="1"><brk id="999999" max="16383" man="1"/></rowBreaks></worksheet>`); err != nil {
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
	hash := printSourceSHA256(t, file)
	w, meta, idx := preparePrintFixture(t, file)
	if idx.RowCount() != records || idx.MaxRow != records || idx.MaxCol != 2 {
		t.Fatal(idx.RowCount(), idx.MaxRow, idx.MaxCol)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelledAt int64
	value, err := w.SourcePrintSettings(ctx, meta, idx, func(done, total int64, _ string) {
		if done >= 8<<20 {
			cancelledAt = done
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || value != nil || cancelledAt == 0 || cancelledAt >= idx.XMLSize || len(w.printCache) != 0 {
		t.Fatal(value, err, cancelledAt)
	}
	started := time.Now()
	value, err = w.SourcePrintSettings(context.Background(), meta, idx, nil)
	readMS := time.Since(started).Milliseconds()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := value.Snapshot()
	if !snapshot.Complete || len(snapshot.Names) != 1 || len(snapshot.RowBreaks) != 1 || snapshot.RowBreaks[0].Index != 999999 || snapshot.Names[0].Areas[0].EndRow != records || snapshot.Properties["headerFooter"]["oddHeader"].Value != "&L原表&P" {
		t.Fatal(snapshot)
	}
	verified := verifyPrintScaleRows(t, w, idx, func(row int) (string, string) { return fmt.Sprintf("%010d", row), "9007199254740993.31" })
	warm, err := w.SourcePrintSettings(context.Background(), meta, idx, func(int64, int64, string) { t.Error("warm print metadata scanned XML") })
	if err != nil || warm != value || printSourceSHA256(t, file) != hash {
		t.Fatal(warm, err)
	}
	writePrintScaleReport(t, map[string]any{"format": "xlsx", "generatedSourceRecords": records, "verifiedSourceRecords": verified, "worksheetXMLBytes": idx.XMLSize, "retainedMetadataBudgetedBytes": value.BudgetBytes(), "explicitReadMsObserved": readMS, "activeScanCancellation": true, "cancelledAtXMLByte": cancelledAt, "warmReadAvoidsXML": true, "sourceSHA256": hash})
}

func TestPrintSettingsBIFF65536RowsAndMaximumBreakLists(t *testing.T) {
	if os.Getenv("EXCELLENS_PRINT_SCALE") != "1" {
		t.Skip("optional full BIFF8 print metadata/source check")
	}
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "full-print.xls")
	stream := appendBIFF(nil, 0x0809, []byte{0, 6, 5, 0})
	position := len(stream) + 4
	stream = appendBIFF(stream, 0x0085, biffCatalogueSheetFixture("Data", 0))
	stream = appendBIFF(stream, 0x000A, nil)
	binary.LittleEndian.PutUint32(stream[position:], uint32(len(stream)))
	stream = appendBIFF(stream, 0x0809, []byte{0, 6, 0x10, 0})
	for row := 0; row < formats.BIFFRows; row++ {
		label := make([]byte, 6)
		binary.LittleEndian.PutUint16(label, uint16(row))
		label = append(label, xlsU16(fmt.Sprintf("%010d", row+1))...)
		stream = appendBIFF(stream, 0x0204, label)
		number := make([]byte, 14)
		binary.LittleEndian.PutUint16(number, uint16(row))
		binary.LittleEndian.PutUint16(number[2:], 1)
		binary.LittleEndian.PutUint64(number[6:], math.Float64bits(float64(row+1)+.5))
		stream = appendBIFF(stream, 0x0203, number)
	}
	stream = appendBIFF(stream, 0x00A1, biffPrintSetupFixture(2))
	stream = appendBIFF(stream, 0x0014, xlsU16("&L原表&P"))
	horizontal := binary.LittleEndian.AppendUint16(nil, formats.PrintBIFFBreaks)
	for at := 1; at <= formats.PrintBIFFBreaks; at++ {
		for _, value := range []int{at * 60, 0, formats.ExcelColumns - 1} {
			horizontal = binary.LittleEndian.AppendUint16(horizontal, uint16(value))
		}
	}
	vertical := binary.LittleEndian.AppendUint16(nil, formats.BIFFColumns-1)
	for at := 1; at < formats.BIFFColumns; at++ {
		for _, value := range []int{at, 0, formats.BIFFRows - 1} {
			vertical = binary.LittleEndian.AppendUint16(vertical, uint16(value))
		}
	}
	stream = appendBIFF(stream, 0x001B, horizontal)
	stream = appendBIFF(stream, 0x001A, vertical)
	stream = appendBIFF(stream, 0x000A, nil)
	writeBIFFCFBFixture(t, file, stream)
	hash := printSourceSHA256(t, file)
	w, meta, idx := preparePrintFixture(t, file)
	if idx.RowCount() != formats.BIFFRows || idx.MaxRow != formats.BIFFRows {
		t.Fatal(idx.RowCount(), idx.MaxRow)
	}
	value, err := w.SourcePrintSettings(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := value.Snapshot()
	if !snapshot.Complete || len(snapshot.RowBreaks) != formats.PrintBIFFBreaks || len(snapshot.ColBreaks) != formats.BIFFColumns-1 || value.BudgetBytes() > formats.PrintCacheBytes {
		t.Fatal(snapshot.Complete, len(snapshot.RowBreaks), len(snapshot.ColBreaks), value.BudgetBytes())
	}
	verified := verifyPrintScaleRows(t, w, idx, func(row int) (string, string) { return fmt.Sprintf("%010d", row), strconv.Itoa(row) + ".5" })
	if printSourceSHA256(t, file) != hash {
		t.Fatal("source changed")
	}
	writePrintScaleReport(t, map[string]any{"format": "xls", "generatedSourceRecords": formats.BIFFRows, "verifiedSourceRecords": verified, "rowBreaks": len(snapshot.RowBreaks), "colBreaks": len(snapshot.ColBreaks), "retainedMetadataBudgetedBytes": value.BudgetBytes(), "sourceSHA256": hash})
}

func verifyPrintScaleRows(t *testing.T, w *Workbook, idx *SheetIndex, expected func(int) (string, string)) int {
	t.Helper()
	verified := 0
	positions := func(yield func(int) bool) {
		for at := 0; at < idx.RowCount(); at++ {
			if !yield(at) {
				return
			}
		}
	}
	if err := w.VisitRowsContext(context.Background(), idx, positions, 1, 2, func(row RowData) error {
		id := verified + 1
		text, number := expected(id)
		if row.Number != id || len(row.Cells) != 2 || row.Cells[0].Ref != fmt.Sprintf("A%d", id) || row.Cells[0].Type != "inlineStr" || row.Cells[0].Raw != text || row.Cells[1].Ref != fmt.Sprintf("B%d", id) || row.Cells[1].Type != "n" || row.Cells[1].Raw != number {
			return fmt.Errorf("print source row changed: %d: %+v", id, row)
		}
		verified++
		return nil
	}); err != nil || verified != idx.RowCount() {
		t.Fatal(verified, err)
	}
	return verified
}

func printSourceSHA256(t *testing.T, file string) string {
	t.Helper()
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

func writePrintScaleReport(t *testing.T, report map[string]any) {
	t.Helper()
	report["version"], report["result"], report["sourceUnchanged"] = buildinfo.Version, "PASS", true
	report["performanceComparison"], report["desktopManualAcceptance"], report["officeWpsAcceptance"] = false, false, false
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
	if target := os.Getenv("EXCELLENS_PRINT_SCALE_REPORT"); target != "" {
		if err := os.WriteFile(target, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
