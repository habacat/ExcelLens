package reconcile

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func TestRelationOriginalSourceOrderSurvivesKeysSparseRowsAndDuplicateExpansion(t *testing.T) {
	left := relationFixture([]Value{text("z"), text("first")}, []Value{text(""), text("invalid")}, []Value{text("a"), text("third")}, []Value{text("z"), text("fourth")}, []Value{text("missing"), text("fifth")})
	right := relationFixture([]Value{text("z"), text("right first")}, []Value{text("right only"), text("right second")}, []Value{text("a"), text("right third")}, []Value{text("z"), text("right fourth")})
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	want := [][2]int{{2, 2}, {2, 11}, {5, 0}, {8, 8}, {11, 2}, {11, 11}, {14, 0}}
	got := [][2]int{}
	for _, row := range relationCollect(t, p, "left") {
		pair := [2]int{row.Left.Number, 0}
		if row.Right != nil {
			pair[1] = row.Right.Number
		}
		if row.Left.Number == 5 && (row.Status != "invalid_key" || row.Reason == "") {
			t.Fatal("invalid source row lost its real state", row)
		}
		got = append(got, pair)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("original sparse coordinates were reordered by encoded key", got, want)
	}
	rows := relationCollect(t, p, "right_unmatched")
	if len(rows) != 1 || rows[0].Right.Number != 5 || rows[0].Left != nil {
		t.Fatal(rows)
	}
}

func TestRelationEachModePreviewMatchesFirstOriginalOrderRowsOfCompleteExtraction(t *testing.T) {
	var input [2][][]Value
	for side, count := range []int{360, 211} {
		for i := 0; i < count; i++ {
			key := fmt.Sprintf("key-%d", (i*37+side*3)%29)
			if i%31 == 0 {
				key = ""
			}
			input[side] = append(input[side], []Value{text(key), text(fmt.Sprintf("%d/%d", side, i))})
		}
	}
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), relationFixture(input[0]...), relationFixture(input[1]...), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i, mode := range relationModes {
		complete := relationCollect(t, p, mode)
		preview, err := p.Preview(context.Background(), mode)
		if err != nil {
			t.Fatal(mode, err)
		}
		limit := min(formats.RelationPreviewRows, len(complete))
		if len(preview.Rows) != limit || len(p.previews[i]) != limit || preview.Total != int64(len(complete)) || preview.Truncated != (len(complete) > limit) {
			t.Fatal(mode, len(preview.Rows), limit, preview.Total)
		}
		for at := range preview.Rows {
			if !reflect.DeepEqual(preview.Rows[at], complete[at]) {
				t.Fatal("preview prefix differs from complete original-order output", mode, at)
			}
		}
	}
}

func TestRelationTenBillionCombinationsKeepOnlyFixedPreviewMetadata(t *testing.T) {
	const count = 100_000
	var reads, readRows int
	makeSource := func() Source {
		makeRecord := func(pos int, columns []int) Record {
			row := Record{Position: pos, Number: pos + 1, Values: make(map[int]Value, len(columns))}
			for _, col := range columns {
				if col == 1 {
					row.Values[col] = text("same")
				} else {
					row.Values[col] = text("value")
				}
			}
			return row
		}
		return Source{Info: SourceInfo{Total: count, Fields: []Field{{Column: 1}, {Column: 2}}}, SourceRows: count, Check: func(ctx context.Context) error { return ctx.Err() },
			Scan: func(ctx context.Context, columns []int, visit func(Record) error) error {
				for pos := 0; pos < count; pos++ {
					if err := visit(makeRecord(pos, columns)); err != nil {
						return err
					}
				}
				return ctx.Err()
			}, Read: func(ctx context.Context, positions iter.Seq[int], columns []int, visit func(Record) error) error {
				reads++
				for pos := range positions {
					readRows++
					if err := visit(makeRecord(pos, columns)); err != nil {
						return err
					}
				}
				return ctx.Err()
			}}
	}
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), makeSource(), makeSource(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Counts().InnerRows != 10_000_000_000 || reads != 2 || readRows != count*2 {
		t.Fatal(p.Counts(), reads, readRows)
	}
	for _, candidates := range p.previews {
		if len(candidates) > formats.RelationPreviewRows {
			t.Fatal("candidate heap expanded with result size")
		}
	}
	preview, err := p.Preview(context.Background(), "inner")
	if err != nil || len(preview.Rows) != 100 || !preview.Truncated || preview.Total != 10_000_000_000 || reads != 4 || readRows != count*2+101 {
		t.Fatal(len(preview.Rows), err, reads, readRows)
	}
	for i, row := range preview.Rows {
		if row.Left.Number != 1 || row.Right.Number != i+1 || row.RightMatches != count {
			t.Fatal("huge join preview is not the first original-order prefix", row)
		}
	}
	emitted := false
	if _, err := p.Stream(context.Background(), "inner", formats.RelationOutputRows, func(RelationRow) error { emitted = true; return nil }); err == nil || emitted || reads != 4 {
		t.Fatal("unbounded full output was admitted", err, emitted, reads)
	}
}

func TestRelationOriginalOrderCodecRefusesMalformedPositionsCountsAndState(t *testing.T) {
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), relationFixture([]Value{text("key"), text("left")}), relationFixture([]Value{text("key"), text("right")}), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, kind := range []string{"truncated", "both absent", "left outside", "right outside", "unknown state", "excessive matches", "matched without left", "matched without count", "unmatched with matches", "unmatched with both sides", "invalid with matches", "invalid with both sides"} {
		var raw [45]byte
		binary.BigEndian.PutUint32(raw[:], 1)
		binary.BigEndian.PutUint32(raw[4:], 1)
		raw[8] = 1
		binary.BigEndian.PutUint32(raw[9:], 1)
		encoded := raw[:]
		switch kind {
		case "truncated":
			encoded = encoded[:44]
		case "both absent":
			clear(encoded[:8])
		case "left outside":
			binary.BigEndian.PutUint32(raw[:], 2)
		case "right outside":
			binary.BigEndian.PutUint32(raw[4:], 2)
		case "unknown state":
			raw[8] = 99
		case "excessive matches":
			binary.BigEndian.PutUint32(raw[9:], 2)
		case "matched without left":
			clear(raw[:4])
		case "matched without count":
			clear(raw[9:13])
		case "unmatched with matches":
			clear(raw[:4])
			raw[8] = 2
		case "unmatched with both sides":
			raw[8] = 2
			clear(raw[9:13])
		case "invalid with matches":
			clear(raw[:4])
			raw[8] = 3
		case "invalid with both sides":
			raw[8] = 3
			clear(raw[9:13])
		}
		if _, err := p.decodePair(string(encoded)); err == nil {
			t.Fatal(kind)
		}
	}
}

// Use the real shared disk sorter at its minimum supported memory budget.
// The independent oracle is a dense left/right position walk, with no key
// encoding, sorting, group iterator or production preview candidates.
func TestRelationOriginalOrderSpillMergeCancellationAndDiskFailureReleaseRuns(t *testing.T) {
	leftRows, rightRows := make([][]Value, 211), make([][]Value, 173)
	for side, rows := range [][][]Value{leftRows, rightRows} {
		for i := range rows {
			rows[i] = []Value{text(fmt.Sprintf("key-%d", (i*37+side*3)%5)), text(fmt.Sprint(i))}
		}
	}
	dir := t.TempDir()
	p, err := PrepareRelation(context.Background(), dir, relationQuery(), relationFixture(leftRows...), relationFixture(rightRows...), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var expected [][2]int
	for li, left := range leftRows {
		for ri, right := range rightRows {
			if left[0].Raw == right[0].Raw {
				expected = append(expected, [2]int{li, ri})
			}
		}
	}
	for _, outcome := range []string{"complete", "cancel", "consumer stops", "disk failure", "count short", "count long"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			orders, err := p.borrow()
			if err != nil {
				t.Fatal(err)
			}
			defer orders[0].Close()
			defer orders[1].Close()
			expectedTotal := p.Counts().InnerRows
			sortDir := dir
			if outcome == "disk failure" {
				sortDir = filepath.Join(t.TempDir(), "file-not-directory")
				if err := os.WriteFile(sortDir, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				p.dir = sortDir
				defer func() { p.dir = dir }()
			}
			if outcome == "count short" {
				expectedTotal--
			}
			if outcome == "count long" {
				expectedTotal++
			}
			var count int
			var failure error
			for pair, err := range p.sortedPairs(ctx, orders, "inner", expectedTotal, 32<<10) {
				if err != nil {
					failure = err
					break
				}
				if count >= len(expected) || pair.positions != expected[count] || pair.status != "matched" {
					t.Fatal("disk merge changed original pair order or status", count, pair)
				}
				count++
				if count == 17 && outcome == "cancel" {
					cancel()
				}
				if count == 17 && outcome == "consumer stops" {
					break
				}
			}
			switch outcome {
			case "complete":
				if failure != nil || count != len(expected) {
					t.Fatal(count, failure)
				}
			case "cancel":
				if !errors.Is(failure, context.Canceled) || count != 17 {
					t.Fatal("cancellation was lost or extra rows emitted", count, failure)
				}
			case "consumer stops":
				if failure != nil || count != 17 {
					t.Fatal(count, failure)
				}
			default:
				if failure == nil || count != 0 {
					t.Fatal("disk/count failure published a partial result", count, failure)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 || workbook.QueryFilesInUse(dir) {
				t.Fatal("pair sort failed to return temporary disk/file reservations", entries, err)
			}
		})
	}
}

func TestRelationBinaryPairMetadataKeepsBillionthCoordinatesThroughSortRuns(t *testing.T) {
	dir := t.TempDir()
	p := &Relation{sources: [2]Source{{SourceRows: formats.ExternalRows}, {SourceRows: formats.ExternalRows, Info: SourceInfo{Total: formats.ExternalRows}}}}
	const count = 2001
	var got int
	err := workbook.SortKeyStream(context.Background(), dir, count, 32<<10, func(ctx context.Context, emit func(workbook.KeyRecord) error) error {
		for i := 0; i < count; i++ {
			var raw [45]byte
			binary.BigEndian.PutUint32(raw[:], uint32(formats.ExternalRows-i))
			binary.BigEndian.PutUint32(raw[4:], uint32(formats.ExternalRows))
			raw[8] = 1
			binary.BigEndian.PutUint32(raw[9:], uint32(formats.ExternalRows))
			copy(raw[13:], strings.Repeat("\xff\x00", 16))
			if err := emit(workbook.KeyRecord{Position: i, Key: string(raw[:])}); err != nil {
				return err
			}
		}
		return nil
	}, func(record workbook.KeyRecord) error {
		pair, err := p.decodePair(record.Key)
		if err != nil {
			return err
		}
		if pair.positions != [2]int{formats.ExternalRows - count + got, formats.ExternalRows - 1} || pair.rightCount != formats.ExternalRows || pair.status != "matched" || string(pair.keyHash[:]) != strings.Repeat("\xff\x00", 16) {
			return fmt.Errorf("binary source coordinates/count/hash changed at %d: %+v", got, pair)
		}
		got++
		return nil
	}, nil)
	if err != nil || got != count || workbook.QueryFilesInUse(dir) {
		t.Fatal(got, err)
	}
}
