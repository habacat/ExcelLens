package workbook

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"excellens/internal/formats"
)

func TestPrintGeometryMillionOOXMLAlternatingSourceAndActiveCancellation(t *testing.T) {
	if os.Getenv("EXCELLENS_PRINT_GEOMETRY_SCALE") != "1" {
		t.Skip("optional actual million-row source/geometry check")
	}
	isolateTestStorage(t)
	const records = 1_000_000
	file := filepath.Join(t.TempDir(), "million-geometry.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, value := range printTestParts("", "") {
		if value == "" {
			continue
		}
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, value); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := z.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriterSize(entry, 1<<20)
	if _, err := io.WriteString(writer, `<worksheet xmlns="`+catalogueTestNS+`"><sheetFormatPr defaultRowHeight="15" baseColWidth="8"/><sheetData>`); err != nil {
		t.Fatal(err)
	}
	var expected int64
	for row := 1; row <= records; row++ {
		height, units := "0.1", int64(100_000_000)
		if row%2 == 0 {
			height, units = "2000", 2_000_000_000_000
		}
		hidden := ""
		if row%7 == 0 {
			hidden = ` hidden="1"`
		} else {
			expected += units
		}
		if _, err := fmt.Fprintf(writer, `<row r="%d" ht="%s"%s><c r="A%d" t="inlineStr"><is><t>%010d</t></is></c><c r="B%d" t="n"><v>9007199254740993.31</v></c></row>`, row, height, hidden, row, row, row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := io.WriteString(writer, `</sheetData></worksheet>`); err != nil {
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
	w, meta, idx := preparePrintGeometryFixture(t, file)
	if idx.RowCount() != records || idx.MaxRow != records {
		t.Fatal(idx.RowCount(), idx.MaxRow)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelledAt int64
	value, err := w.SourcePrintGeometry(ctx, meta, idx, func(done, total int64, _ string) {
		if done >= 8<<20 {
			cancelledAt = done
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || value != nil || cancelledAt == 0 || cancelledAt >= idx.XMLSize || len(w.printGeometryCache) != 0 {
		t.Fatal(value, err, cancelledAt)
	}
	started := time.Now()
	value, err = w.SourcePrintGeometry(context.Background(), meta, idx, nil)
	readMS := time.Since(started).Milliseconds()
	if err != nil {
		t.Fatal(err)
	}
	if !value.Summary().Complete || value.Summary().RowSegments != records || value.BudgetBytes() > formats.PrintGeometryBytes {
		t.Fatal(value.Summary(), value.BudgetBytes())
	}
	assertPrintExtent(t, value, "row", 1, records, expected, false)
	assertPrintExtent(t, value, "row", 999997, 999997, 100_000_000, false)
	assertPrintExtent(t, value, "row", 999999, 999999, 0, false)
	assertPrintExtent(t, value, "row", 999999, 1000000, 2_000_000_000_000, false)
	verified := verifyPrintScaleRows(t, w, idx, func(row int) (string, string) { return fmt.Sprintf("%010d", row), "9007199254740993.31" })
	warm, err := w.SourcePrintGeometry(context.Background(), meta, idx, func(int64, int64, string) { t.Error("warm geometry opened XML") })
	if err != nil || warm != value || printSourceSHA256(t, file) != hash {
		t.Fatal(warm, err)
	}
	writePrintScaleReport(t, map[string]any{"format": "xlsx", "generatedSourceRecords": records, "verifiedSourceRecords": verified, "sourceDimensionSegments": value.Summary().RowSegments, "fullGeometryUnits": expected, "geometryBudgetedBytes": value.BudgetBytes(), "explicitReadMsObserved": readMS, "activeScanCancellation": true, "cancelledAtXMLByte": cancelledAt, "warmReadAvoidsXML": true, "sourceSHA256": hash, "worksheetXMLBytes": idx.XMLSize})
}

func TestPrintGeometryBIFF65536SourceRowsAndTwipExtents(t *testing.T) {
	if os.Getenv("EXCELLENS_PRINT_GEOMETRY_SCALE") != "1" {
		t.Skip("optional full BIFF8 source/geometry check")
	}
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "full-geometry.xls")
	stream := appendBIFF(nil, 0x0809, []byte{0, 6, 5, 0})
	position := len(stream) + 4
	stream = appendBIFF(stream, 0x0085, biffCatalogueSheetFixture("Data", 0))
	stream = appendBIFF(stream, 0x000A, nil)
	binary.LittleEndian.PutUint32(stream[position:], uint32(len(stream)))
	stream = appendBIFF(stream, 0x0809, []byte{0, 6, 0x10, 0})
	stream = appendBIFF(stream, 0x0225, []byte{0, 0, 44, 1})
	stream = appendBIFF(stream, 0x0055, []byte{8, 0})
	var expected int64
	for row := 0; row < formats.BIFFRows; row++ {
		height := uint16(2)
		if row%2 == 1 {
			height = 8192
		}
		definition := make([]byte, 16)
		binary.LittleEndian.PutUint16(definition, uint16(row))
		binary.LittleEndian.PutUint16(definition[6:], height)
		definition[13] = 1
		if (row+1)%7 == 0 {
			definition[12] = 0x20
		} else {
			expected += int64(height) * 50_000_000
		}
		stream = appendBIFF(stream, 0x0208, definition)
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
	stream = appendBIFF(stream, 0x000A, nil)
	writeBIFFCFBFixture(t, file, stream)
	hash := printSourceSHA256(t, file)
	w, meta, idx := preparePrintGeometryFixture(t, file)
	value, err := w.SourcePrintGeometry(context.Background(), meta, idx, nil)
	if err != nil || !value.Summary().Complete || value.Summary().RowSegments != formats.BIFFRows {
		t.Fatal(value, err)
	}
	assertPrintExtent(t, value, "row", 1, formats.BIFFRows, expected, false)
	verified := verifyPrintScaleRows(t, w, idx, func(row int) (string, string) { return fmt.Sprintf("%010d", row), strconv.Itoa(row) + ".5" })
	if printSourceSHA256(t, file) != hash {
		t.Fatal("source changed")
	}
	writePrintScaleReport(t, map[string]any{"format": "xls", "generatedSourceRecords": formats.BIFFRows, "verifiedSourceRecords": verified, "sourceDimensionSegments": value.Summary().RowSegments, "fullGeometryUnits": expected, "geometryBudgetedBytes": value.BudgetBytes(), "sourceSHA256": hash})
}
