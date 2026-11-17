package reconcile

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestRelationUnmatchedAndInvalidRowsHaveZeroRightMatchesInPreviewStreamAndCSV(t *testing.T) {
	left := relationFixture([]Value{text("same"), text("L1")}, []Value{text("same"), text("L2")}, []Value{text("left only"), text("L3")}, []Value{text(""), text("L4")})
	right := relationFixture([]Value{text("same"), text("R1")}, []Value{text("right only"), text("R2")}, []Value{text("same"), text("R3")}, []Value{text("right only"), text("R4")}, []Value{text("same"), text("R5")}, []Value{text(""), text("R6")})
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, mode := range relationModes {
		t.Run(mode, func(t *testing.T) {
			preview, err := p.Preview(context.Background(), mode)
			if err != nil {
				t.Fatal(err)
			}
			check := func(row RelationRow) error {
				expected := int64(0)
				if row.Status == "matched" {
					expected = 3
				}
				if row.RightMatches != expected {
					return fmt.Errorf("%s row reports %d right matches; want %d", row.Status, row.RightMatches, expected)
				}
				return nil
			}
			for _, row := range preview.Rows {
				if err := check(row); err != nil {
					t.Fatal("preview:", err)
				}
			}
			var dst bytes.Buffer
			writer, err := NewRelationCSVWriter(&dst, p, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Stream(context.Background(), mode, 100, func(row RelationRow) error {
				if err := check(row); err != nil {
					return err
				}
				return writer.Write(row)
			})
			if err != nil || writer.Flush() != nil {
				t.Fatal("stream:", err)
			}
			rows, err := csv.NewReader(&dst).ReadAll()
			if err != nil || len(rows) != len(preview.Rows)+1 {
				t.Fatal(rows, err)
			}
			for _, row := range rows[1:] {
				expected := 0
				if row[1] == "已匹配" {
					expected = 3
				}
				if row[3] != strconv.Itoa(expected) {
					t.Fatal("CSV has a false right-side match count", row)
				}
			}
		})
	}
}

func TestRelationCSVIndependentReopenPreservesExactValuesProvenanceAndAbsentSide(t *testing.T) {
	left := relationFixture([]Value{text("001"), text("=SUM(1,2)\n原文")}, []Value{text("left"), number("9007199254740993.31")}, []Value{text("cache"), {Type: "n", Exists: true, MissingCache: true}})
	right := relationFixture([]Value{text("001"), text("带,引号\"值")}, []Value{text("001"), text("第二条")})
	left.Info.ID, left.Info.Name, left.Info.Sheet, left.Info.Stamp = "left-id", "left.csv", "数据", "snapshot-left"
	right.Info.ID, right.Info.Name, right.Info.Sheet, right.Info.Stamp = "right-id", "right.csv", "数据", "snapshot-right"
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for _, protect := range []bool{false, true} {
		var dst bytes.Buffer
		var transform func(string) string
		if protect {
			transform = func(value string) string {
				if strings.HasPrefix(value, "=") {
					return "'" + value
				}
				return value
			}
		}
		w, err := NewRelationCSVWriter(&dst, p, transform)
		if err != nil {
			t.Fatal(err)
		}
		n, err := p.Stream(context.Background(), "left", 4, w.Write)
		if err != nil || n != 4 || w.Flush() != nil || w.Rows != 4 {
			t.Fatal(n, err, w.Rows)
		}
		rows, err := csv.NewReader(&dst).ReadAll()
		if err != nil || len(rows) != 5 {
			t.Fatal(rows, err)
		}
		columns := map[string]int{}
		for i, header := range rows[0] {
			columns[header] = i
		}
		valueName := "左 A 1"
		amountName := "左 B 2"
		if protect {
			valueName += "（文本保护）"
			amountName += "（文本保护）"
		}
		var firstCopies, missing, cache int
		for _, row := range rows[1:] {
			if row[columns["左来源身份"]] != "left-id" || row[columns["左快照"]] != "snapshot-left" || row[columns["左源列(JSON)"]] != "[1,2]" {
				t.Fatal("CSV provenance changed", row)
			}
			switch row[columns[valueName]] {
			case "001":
				firstCopies++
				expected := "=SUM(1,2)\n原文"
				if protect {
					expected = "'" + expected
				}
				if row[columns[amountName]] != expected || row[columns["右记录存在"]] != "true" || row[columns["左源行"]] != "2" {
					t.Fatal("quoted/formula-like source or source row changed", row)
				}
			case "left":
				missing++
				if row[columns[amountName]] != "9007199254740993.31" || row[columns["右记录存在"]] != "false" || row[columns["右单元格存在(JSON)"]] != "" || row[columns["左读取类型(JSON)"]] != `["s","n"]` {
					t.Fatal("absent source was confused with an empty source value", row)
				}
			case "cache":
				cache++
				var flags []bool
				if json.Unmarshal([]byte(row[columns["左缺少缓存(JSON)"]]), &flags) != nil || len(flags) != 2 || !flags[1] {
					t.Fatal("missing cache lost", row)
				}
			}
		}
		if firstCopies != 2 || missing != 1 || cache != 1 {
			t.Fatal(firstCopies, missing, cache)
		}
	}
}

type relationFailWriter struct{}

func TestRelationCSVColumnOrderAndIncompleteNonKeyStateRemainAligned(t *testing.T) {
	left := relationFixture([]Value{text("key"), {Raw: "prefix", Type: "s", Exists: true, Truncated: true}, {Raw: "", Type: "s", Exists: true}})
	right := relationFixture([]Value{text("key"), text("right")})
	q := relationQuery()
	q.LeftColumns = []int{2, 3, 1}
	p, err := PrepareRelation(context.Background(), t.TempDir(), q, left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var dst bytes.Buffer
	w, err := NewRelationCSVWriter(&dst, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stream(context.Background(), "inner", 1, w.Write); err != nil || w.Flush() != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&dst).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	lookup := map[string]string{}
	for i, name := range rows[0] {
		lookup[name] = rows[1][i]
	}
	if lookup["左源列(JSON)"] != "[2,3,1]" || lookup["左原文截断(JSON)"] != "[true,false,false]" || lookup["左单元格存在(JSON)"] != "[true,true,true]" || lookup["左 B 2"] != "prefix" || lookup["左 C 3"] != "" || lookup["左 A 1"] != "key" {
		t.Fatal(lookup)
	}
}

func (relationFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRelationCSVReportsWriteFailuresAndRejectsInventedRecords(t *testing.T) {
	source := relationFixture([]Value{text("one"), text("text")})
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), source, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	w, err := NewRelationCSVWriter(relationFailWriter{}, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	if _, err := NewRelationCSVWriter(io.Discard, nil, nil); err == nil {
		t.Fatal("CSV without preflight accepted")
	}
	w, err = NewRelationCSVWriter(io.Discard, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []RelationRow{{Sequence: 1, Status: "matched"}, {Sequence: 2, Status: "matched", Left: &Record{Number: 2}}, {Sequence: 1, Status: "unknown", Left: &Record{Number: 2}}, {Sequence: 1, Status: "matched", Left: &Record{Number: 0}}} {
		if err := w.Write(row); err == nil {
			t.Fatal("invented CSV record accepted", row)
		}
	}
}
