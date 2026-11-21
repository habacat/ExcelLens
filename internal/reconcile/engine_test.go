package reconcile

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"reflect"
	"strings"
	"testing"
)

func text(raw string) Value   { return Value{Raw: raw, Display: raw, Type: "s", Exists: true} }
func number(raw string) Value { return Value{Raw: raw, Display: raw, Type: "n", Exists: true} }

func TestAmountToleranceKeepsExactDifferencesAndLegacyBooleanRules(t *testing.T) {
	left := fixtureSource([]Value{text("a"), number("9007199254740993")},
		[]Value{text("b"), number("9007199254740993.009")}, []Value{text("c"), number("9007199254740993.01")},
		[]Value{text("d"), number("9007199254740992.989")})
	right := fixtureSource([]Value{text("a"), number("9007199254740993")}, []Value{text("b"), number("9007199254740993")},
		[]Value{text("c"), number("9007199254740993")}, []Value{text("d"), number("9007199254740993")})
	q := Query{Keys: []Pair{{Left: 1, Right: 1}}, Fields: []Pair{{Left: 2, Right: 2, Mode: "number", Tolerance: "0.01"}}, IncludeEqual: true}
	result, err := Run(t.Context(), t.TempDir(), q, left, right, Sinks{}, nil)
	if err != nil || result.Counts.Equal != 1 || result.Counts.WithinTolerance != 2 || result.Counts.Different != 1 {
		t.Fatal(result.Counts, err)
	}
	for i, want := range []string{"0", "0.009", "0.01", "-0.011"} {
		c := result.Samples[i].Comparisons[0]
		if c.Delta != want || c.WithinTolerance != (i == 1 || i == 2) || c.Equal != (i == 0) {
			t.Fatal(c)
		}
	}
	q.RuleVersion = 1
	if Validate(q, left, right) == nil {
		t.Fatal("legacy rules silently accepted a tolerance")
	}
	q.RuleVersion = 0
	for _, invalid := range []string{"-0.01", "1e2", "1/2", "."} {
		q.Fields[0].Tolerance = invalid
		if Validate(q, left, right) == nil {
			t.Fatal(invalid)
		}
	}
	q.Keys[0].Tolerance = "0.01"
	q.Fields = nil
	if Validate(q, left, right) == nil {
		t.Fatal("key accepted an amount tolerance")
	}
	q.Keys[0].Tolerance = ""
	boolValue := func(raw string) Value { return Value{Raw: raw, Type: "b", Exists: true} }
	left, right = fixtureSource([]Value{boolValue("TRUE")}), fixtureSource([]Value{boolValue("true")})
	for _, rules := range []int{1, 2} {
		q.RuleVersion = rules
		r, err := Run(t.Context(), t.TempDir(), q, left, right, Sinks{}, nil)
		if err != nil || rules == 1 && r.Counts.InvalidKeys != 2 || rules == 2 && (r.Counts.Equal != 1 || r.Samples[0].Left.Values[1].Raw != "TRUE") {
			t.Fatal(r.Counts, err)
		}
	}
}

func fixtureSource(rows ...[]Value) Source {
	columns := 2
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	var fields []Field
	for col := 1; col <= columns; col++ {
		fields = append(fields, Field{Column: col, Name: fmt.Sprint(col)})
	}
	makeRecord := func(pos int, needed []int) Record {
		values := make(map[int]Value)
		for _, col := range needed {
			if col <= len(rows[pos]) {
				values[col] = rows[pos][col-1]
			}
		}
		return Record{Position: pos, Number: pos*3 + 2, Values: values}
	}
	return Source{Info: SourceInfo{ID: "source", Fields: fields, Total: len(rows)}, SourceRows: len(rows),
		Scan: func(ctx context.Context, need []int, visit func(Record) error) error {
			for i := range rows {
				if err := visit(makeRecord(i, need)); err != nil {
					return err
				}
			}
			return ctx.Err()
		}, Read: func(ctx context.Context, positions iter.Seq[int], need []int, visit func(Record) error) error {
			for pos := range positions {
				if err := visit(makeRecord(pos, need)); err != nil {
					return err
				}
			}
			return ctx.Err()
		}}
}

func TestReconcileExactTypedValuesMissingKeysAndDuplicateProvenance(t *testing.T) {
	left := fixtureSource(
		[]Value{text("same"), number("0.1")},
		[]Value{text("money"), number("9007199254740993.31")},
		[]Value{text("left"), text("00123")},
		[]Value{text("duplicate"), number("1")},
		[]Value{text("duplicate"), number("2")},
		[]Value{text(""), number("0")},
		[]Value{{Exists: true, Type: "n", MissingCache: true}, number("1")},
		[]Value{text("cache"), {Exists: true, Type: "n", MissingCache: true}},
	)
	right := fixtureSource(
		[]Value{text("money"), number("9007199254740993.30")},
		[]Value{text("same"), number("1e-1")},
		[]Value{text("right"), text("00123")},
		[]Value{text("duplicate"), number("3")},
		[]Value{text("duplicate"), number("4")},
		[]Value{text("duplicate"), number("5")},
		[]Value{text(""), number("0")},
		[]Value{text("cache"), number("0")},
	)
	query := Query{Keys: []Pair{{Left: 1, Right: 1}}, Fields: []Pair{{Left: 2, Right: 2}}}
	var duplicates []string
	var groups []Group
	result, err := Run(context.Background(), t.TempDir(), query, left, right, Sinks{
		Group: func(g Group) error { groups = append(groups, g); return nil },
		Duplicate: func(_ string, side string, record Record) error {
			duplicates = append(duplicates, fmt.Sprintf("%s:%d:%s", side, record.Number, record.Values[2].Raw))
			return nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Counts{Equal: 1, Different: 1, LeftOnly: 1, RightOnly: 1, Duplicate: 1, Review: 1, InvalidKeys: 3, LeftRecords: 8, RightRecords: 8}
	if result.Counts != want || len(result.Samples) != 8 || len(groups) != 8 {
		t.Fatal("incorrect record/key accounting", result.Counts, len(result.Samples), len(groups))
	}
	if !reflect.DeepEqual(duplicates, []string{"left:11:1", "left:14:2", "right:11:3", "right:14:4", "right:17:5"}) {
		t.Fatal("duplicates were lost or expanded into a Cartesian product", duplicates)
	}
	for _, group := range result.Samples {
		if group.Status == "different" && group.Comparisons[0].Delta != "0.01" {
			t.Fatal("exact monetary difference lost", group)
		}
		if group.Left != nil && group.Left.Number == 20 && !strings.Contains(group.Reason, "缺少缓存") {
			t.Fatal("missing formula cache was disguised as an empty key", group)
		}
	}
}

func TestReconcileKeyModesAreExplicitAndUnicodeFoldMatchesFilter(t *testing.T) {
	left := fixtureSource([]Value{text("0002"), text("25%")}, []Value{text(" long ſ "), text("(1,234.50)")})
	right := fixtureSource([]Value{number("2"), number("0.25")}, []Value{text("LONG S"), number("-1234.5")})
	for _, query := range []Query{
		{Keys: []Pair{{Left: 1, Right: 1}}, Fields: []Pair{{Left: 2, Right: 2}}},
		{Keys: []Pair{{Left: 1, Right: 1, Mode: "text", Trim: true, IgnoreCase: true}}, Fields: []Pair{{Left: 2, Right: 2, Mode: "number"}}},
		{Keys: []Pair{{Left: 1, Right: 1, Mode: "number"}}, Fields: []Pair{{Left: 2, Right: 2, Mode: "number"}}},
	} {
		result, err := Run(context.Background(), t.TempDir(), query, left, right, Sinks{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		switch query.Keys[0].Mode {
		case "":
			if result.Counts.LeftOnly != 2 || result.Counts.RightOnly != 2 || result.Counts.Equal != 0 {
				t.Fatal("source types were implicitly coerced", result.Counts)
			}
		case "text":
			if result.Counts.Equal != 1 || result.Counts.LeftOnly != 1 || result.Counts.RightOnly != 1 {
				t.Fatal("explicit text folding did not preserve identifiers", result.Counts)
			}
		case "number":
			if result.Counts.Equal != 1 || result.Counts.InvalidKeys != 2 {
				t.Fatal("explicit numeric key conversion or percent scaling failed", result.Counts)
			}
		}
	}
	if left.Info.Total != 2 {
		t.Fatal("source changed")
	}
}

func TestReconcileCancellationAndSinkFailureNeverReturnPartialSuccess(t *testing.T) {
	var rows [][]Value
	for i := 0; i < 500; i++ {
		rows = append(rows, []Value{text(fmt.Sprint(i)), number("1")})
	}
	query := Query{Keys: []Pair{{Left: 1, Right: 1}}, IncludeEqual: true}
	for _, cancel := range []bool{false, true} {
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		dir := t.TempDir()
		sentinel := errors.New("output interrupted")
		count := 0
		result, err := Run(ctx, dir, query, fixtureSource(rows...), fixtureSource(rows...), Sinks{Group: func(Group) error {
			count++
			if count == 17 {
				if cancel {
					stop()
					return nil
				}
				return sentinel
			}
			return nil
		}}, nil)
		if cancel && !errors.Is(err, context.Canceled) || !cancel && !errors.Is(err, sentinel) || result.Counts.LeftRecords != 0 {
			t.Fatal("failed operation exposed partial success", result, err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("stopped iterators left temporary files", entries)
		}
	}
}

func TestReconcileDetectsSnapshotDriftAndKeepsSamplesBounded(t *testing.T) {
	var rows [][]Value
	for i := 0; i < 121; i++ {
		rows = append(rows, []Value{text(fmt.Sprint(i)), text(strings.Repeat("😀", 300))})
	}
	left, right := fixtureSource(rows...), fixtureSource()
	query := Query{Keys: []Pair{{Left: 1, Right: 1}}, Fields: []Pair{{Left: 2, Right: 2}}}
	result, err := Run(context.Background(), t.TempDir(), query, left, right, Sinks{}, nil)
	if err != nil || result.Counts.LeftOnly != 121 || len(result.Samples) != MaxSamples || !result.SamplesTruncated {
		t.Fatal("sample count or completeness lost", result.Counts, len(result.Samples), err)
	}
	if !result.Samples[0].TextTruncated || len([]rune(result.Samples[0].Left.Values[2].Raw)) != SampleChars {
		t.Fatal("Unicode excerpts split characters or did not mark truncation")
	}
	read := left.Read
	left.Read = func(ctx context.Context, positions iter.Seq[int], columns []int, visit func(Record) error) error {
		return read(ctx, positions, columns, func(record Record) error { record.Values[1] = text("changed"); return visit(record) })
	}
	if _, err := Run(context.Background(), t.TempDir(), query, left, right, Sinks{}, nil); err == nil || !strings.Contains(err.Error(), "发生变化") {
		t.Fatal("a changed key in the second pass was accepted", err)
	}
}
