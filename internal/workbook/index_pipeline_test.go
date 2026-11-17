package workbook

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// Covers both stream sizes and verifies byte-exact cache output and row
// offsets. A very long text payload also crosses many bufio/pipe boundaries.
func TestScanAndSpoolWorksheetMatchesSerial(t *testing.T) {
	for _, size := range []int{64 << 10, 33 << 20} {
		if testing.Short() && size > 1<<20 {
			continue
		}
		src := []byte(`<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>` + strings.Repeat("X", size) + `</t></is></c></row><row r="3"><c r="C3"><f t="shared" si="12" ref="C3:C4">A3+1</f><v>11</v></c></row></sheetData></worksheet>`)
		var written bytes.Buffer
		rows, anchors, dim, maxRow, maxCol, err := scanAndSpoolWorksheet(bytes.NewReader(src), &written, int64(len(src)), nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(src, written.Bytes()) {
			t.Fatalf("decompressed output changed at size %d", size)
		}
		wantRows, wantAnchors, wantDim, wantMaxRow, wantMaxCol, err := scanWorksheetWithShared(bytes.NewReader(src), int64(len(src)), nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(rows, wantRows) || !reflect.DeepEqual(anchors, wantAnchors) || dim != wantDim || maxRow != wantMaxRow || maxCol != wantMaxCol {
			t.Fatalf("pipeline mismatch at size %d: got rows=%v extent=%s %d %d", size, rows, dim, maxRow, maxCol)
		}
	}
}

func TestScanAndSpoolWorksheetCancellation(t *testing.T) {
	src := []byte(`<worksheet><sheetData><row r="1"><c r="A1"><v>` + strings.Repeat("1", 33<<20) + `</v></c></row></sheetData></worksheet>`)
	cancel := make(chan struct{})
	close(cancel)
	_, _, _, _, _, err := scanAndSpoolWorksheet(bytes.NewReader(src), io.Discard, int64(len(src)), cancel, nil, nil)
	if err == nil {
		t.Fatal("cancelled pipeline returned success")
	}
}

type rejectWriter struct{}

func (rejectWriter) Write([]byte) (int, error) { return 0, errors.New("test disk full") }
func TestScanAndSpoolWorksheetWriterError(t *testing.T) {
	src := []byte(`<worksheet><sheetData><row r="1"><c r="A1"><v>` + strings.Repeat("1", 33<<20) + `</v></c></row></sheetData></worksheet>`)
	_, _, _, _, _, err := scanAndSpoolWorksheet(bytes.NewReader(src), rejectWriter{}, int64(len(src)), nil, nil, nil)
	if err == nil {
		t.Fatal("failed disk write must cancel scan")
	}
}
