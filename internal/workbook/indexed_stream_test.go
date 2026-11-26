package workbook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestIndexedStreamPreReadsBothDirectionsAndBoundsRandomReads(t *testing.T) {
	const count = 2400
	text := strings.Repeat("内容", 256)
	lines := make([]string, count)
	for i := range lines {
		lines[i] = fmt.Sprintf(`<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s-%d</t></is></c></row>`, i+1, i+1, text, i)
	}
	idx := syntheticIndex(t, lines...)
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &Workbook{}
	for _, reverse := range []bool{false, true} {
		stream := indexedStream{f: f, idx: idx}
		refills := 0
		var start, end int64 = -1, -1
		for i := 0; i < count; i++ {
			at := i
			if reverse {
				at = count - 1 - i
			}
			row, err := stream.read(w, at, 1, 1, nil, false, false)
			if err != nil || len(row.Cells) != 1 || row.Cells[0].Raw != fmt.Sprintf("%s-%d", text, at) {
				t.Fatalf("reverse=%v at=%d: %v / %v", reverse, at, row, err)
			}
			if stream.start != start || stream.end != end {
				refills++
				start, end = stream.start, stream.end
			}
		}
		if refills > int(idx.XMLSize/indexedReadAhead)+3 {
			t.Fatalf("reverse=%v reloaded too often: %d for %d bytes", reverse, refills, idx.XMLSize)
		}
	}
	stream := indexedStream{f: f, idx: idx}
	for _, at := range []int{0, count - 1, count / 2, 3} {
		if _, err := stream.read(w, at, 1, 1, nil, false, false); err != nil {
			t.Fatal(err)
		}
		if int64(len(stream.block)) > indexedRandomBlock {
			t.Fatalf("random small row fetched %d bytes", len(stream.block))
		}
	}
}

func TestIndexedStreamRetriesIncompleteRead(t *testing.T) {
	xml := `<row r="1"><c r="A1"><v>42</v></c></row>`
	idx := syntheticIndex(t, xml)
	f, err := os.OpenFile(idx.XMLPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(5); err != nil {
		t.Fatal(err)
	}
	s := indexedStream{f: f, idx: idx}
	w := &Workbook{}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := s.read(w, 0, 1, 1, nil, false, false); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("read %d returned %v", attempt+1, err)
		}
	}
	if _, err := f.WriteAt([]byte(xml), 0); err != nil {
		t.Fatal(err)
	}
	row, err := s.read(w, 0, 1, 1, nil, false, false)
	if err != nil || len(row.Cells) != 1 || row.Cells[0].Raw != "42" {
		t.Fatal(row, err)
	}
}
