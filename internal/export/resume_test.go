package export

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"iter"
	"reflect"
	"testing"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func TestResumePartFramingPreservesEmptyAndLargeSectionsAndRejectsTruncation(t *testing.T) {
	data := bytes.Repeat([]byte("001\r\n\x00"), formats.ExportPartChunkBytes/3)
	var stored bytes.Buffer
	writer := NewPartWriter(&stored)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	empty := NewPartWriter(&stored)
	if err := empty.Close(); err != nil {
		t.Fatal(err)
	}
	input := bytes.NewReader(stored.Bytes())
	got, err := io.ReadAll(NewPartReader(input))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("transport changed complete section bytes", err)
	}
	got, err = io.ReadAll(NewPartReader(input))
	if err != nil || len(got) != 0 || input.Len() != 0 {
		t.Fatal("empty section swallowed its terminator or next section", err)
	}
	for _, bad := range [][]byte{nil, {1, 0}, {5, 0, 0, 0, 'x'}, binary.LittleEndian.AppendUint32(nil, formats.ExportPartChunkBytes+1)} {
		if _, err := io.ReadAll(NewPartReader(bytes.NewReader(bad))); err == nil {
			t.Fatal("incomplete or oversized section was accepted", bad)
		}
	}
}

type resumeRangeProbe struct {
	index  *workbook.SheetIndex
	called *[2]int
	rows   []int
}

func (p resumeRangeProbe) Index() *workbook.SheetIndex { return p.index }
func (p resumeRangeProbe) Len() int                    { return len(p.rows) }
func (p resumeRangeProbe) Rows() iter.Seq[workbook.SourceRowReference] {
	panic("confirmed prefix must not be enumerated")
}
func (p resumeRangeProbe) RowsRange(first, count int) iter.Seq[workbook.SourceRowReference] {
	*p.called = [2]int{first, count}
	return func(yield func(workbook.SourceRowReference) bool) {
		for _, number := range p.rows[first : first+count] {
			position := p.index.FindRowOffset(number)
			if position >= p.index.RowCount() || p.index.RowAt(position).Number != number {
				position = -1
			}
			if !yield(workbook.SourceRowReference{Number: number, Position: position}) {
				return
			}
		}
	}
}

func TestResumeWindowSkipsConfirmedPrefixAndKeepsAbsoluteOutputCoordinates(t *testing.T) {
	wb, s := richExportFixture(t, false)
	var called [2]int
	p := resumeRangeProbe{index: s.Index, called: &called, rows: []int{2, 1, 3}}
	s.Rows, s.Order, s.RowNumbers, s.Filtered, s.Compact = p, nil, nil, true, true
	var original, output []int
	err := forEachRowWindow(context.Background(), wb, s, 1, 2, workbook.RowReadOptions{}, func(source, target int, row workbook.RowData) error {
		original, output = append(original, source), append(output, target)
		return nil
	})
	if err != nil || called != [2]int{1, 2} || !reflect.DeepEqual(original, []int{1, 3}) || !reflect.DeepEqual(output, []int{2, 3}) {
		t.Fatal("window scanned an old prefix or reset output coordinates", called, original, output, err)
	}
}

func TestResumeStateRejectsSkippedBoundaryAndInconsistentCursor(t *testing.T) {
	selections := []Selection{{FirstRow: 1, LastRow: 9001, FirstCol: 1, LastCol: 3}}
	for _, format := range []string{"csv", "tsv", "jsonl", "html", "xlsx", "zipcsv"} {
		opts := Options{Format: format, Mode: "values"}
		state := InitialResumeState(opts)
		if err := ValidateResumeState(state, opts, selections); err != nil {
			t.Fatal(format, err)
		}
		bad := state
		bad.Version++
		if err := ValidateResumeState(bad, opts, selections); !errors.Is(err, ErrResumeState) {
			t.Fatal("unknown cursor version accepted", format, err)
		}
		bad = state
		bad.Commits++
		if ValidateResumeState(bad, opts, selections) == nil {
			t.Fatal("unexplained confirmed output accepted", format)
		}
		part := ResumePart{Before: state, Count: 1}
		if _, err := NextResumeState(part, 0, opts, selections); err == nil {
			t.Fatal("partial record/part boundary accepted", format)
		}
	}
}
