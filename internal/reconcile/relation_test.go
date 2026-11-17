package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math/big"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func relationFixture(rows ...[]Value) Source {
	s := fixtureSource(rows...)
	s.Check = func(ctx context.Context) error { return ctx.Err() }
	return s
}

func relationQuery() RelationQuery {
	return RelationQuery{Keys: []Pair{{Left: 1, Right: 1}}, LeftColumns: []int{1, 2}, RightColumns: []int{1, 2}}
}

func relationCollect(t *testing.T, p *Relation, mode string) []RelationRow {
	t.Helper()
	var out []RelationRow
	n, err := p.Stream(context.Background(), mode, formats.RelationOutputRows, func(row RelationRow) error {
		if row.Left != nil {
			row.Left = ownRecord(*row.Left)
		}
		if row.Right != nil {
			row.Right = ownRecord(*row.Right)
		}
		out = append(out, row)
		return nil
	})
	if err != nil || n != int64(len(out)) {
		t.Fatalf("%s: rows=%d error=%v", mode, n, err)
	}
	for i, row := range out {
		if row.Sequence != int64(i+1) {
			t.Fatal("result sequence was substituted for original row", row)
		}
	}
	return out
}

func TestRelationPreflightExactCartesianCountsTypesAndOriginalCoordinates(t *testing.T) {
	left := relationFixture([]Value{text("dup"), number("9007199254740993.31")}, []Value{text("dup"), number("0.1")}, []Value{text("left"), text("001")}, []Value{text(""), text("empty")}, []Value{number("1"), text("native")}, []Value{text("001"), text("identifier")}, []Value{{Type: "n", Exists: true, MissingCache: true}, text("missing cache")})
	right := relationFixture([]Value{text("dup"), number("1")}, []Value{text("dup"), number("2")}, []Value{text("dup"), number("3")}, []Value{text("right"), text("x")}, []Value{number("1.0"), text("native")}, []Value{text("1"), text("text")}, []Value{text(""), text("empty")})
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	want := RelationCounts{LeftRecords: 7, RightRecords: 7, LeftInvalid: 2, RightInvalid: 1, LeftDuplicateGroups: 1, RightDuplicateGroups: 1, ManyToManyGroups: 1, MatchingGroups: 2, LeftMatched: 3, RightMatched: 4, LeftOnly: 2, RightOnly: 2, InnerRows: 7, LeftJoinRows: 11}
	if p.Counts() != want {
		t.Fatalf("counts=%+v want=%+v", p.Counts(), want)
	}
	rows := relationCollect(t, p, "inner")
	var duplicatePairs []string
	for _, row := range rows {
		if row.Left.Values[1].Raw == "dup" {
			duplicatePairs = append(duplicatePairs, fmt.Sprintf("%d/%d", row.Left.Number, row.Right.Number))
		}
		if row.Left.Position == 0 && row.Left.Values[2].Raw != "9007199254740993.31" {
			t.Fatal("exact source amount changed")
		}
	}
	if !reflect.DeepEqual(duplicatePairs, []string{"2/2", "2/5", "2/8", "5/2", "5/5", "5/8"}) {
		t.Fatal(duplicatePairs)
	}
	for mode, expected := range map[string]int{"left": 11, "left_matched": 3, "left_unmatched": 2, "right_unmatched": 2, "left_invalid": 2, "right_invalid": 1} {
		if rows := relationCollect(t, p, mode); len(rows) != expected {
			t.Fatal(mode, len(rows))
		}
	}
	for _, row := range relationCollect(t, p, "left_unmatched") {
		if row.Status != "unmatched" || row.Reason != "" || row.Right != nil || row.Left.Values[1].Raw == "" {
			t.Fatal("uncertain key was reported as confirmed unmatched", row)
		}
	}
	for _, row := range relationCollect(t, p, "left_invalid") {
		if row.Reason == "" || row.Status != "invalid_key" {
			t.Fatal(row)
		}
	}
}

func TestRelationExplicitCompositeKeysAndOptInValueSemantics(t *testing.T) {
	left := relationFixture([]Value{text(" K "), text("25%")}, []Value{text("s"), text("001")})
	right := relationFixture([]Value{text("k"), number("0.25")}, []Value{text("ſ"), number("1")})
	q := relationQuery()
	q.Keys = []Pair{{Left: 1, Right: 1, Mode: "text", Trim: true, IgnoreCase: true}, {Left: 2, Right: 2, Mode: "number"}}
	p, err := PrepareRelation(context.Background(), t.TempDir(), q, left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Counts().InnerRows != 2 {
		t.Fatal(p.Counts())
	}
	rows := relationCollect(t, p, "inner")
	if rows[0].Left.Values[1].Raw != " K " || rows[0].Left.Values[2].Raw != "25%" || rows[1].Left.Values[2].Raw != "001" || rows[1].Left.Values[2].Type != "s" {
		t.Fatal("explicit key interpretation changed source values", rows)
	}
	q.Keys[0].Left = 2
	copy := p.Query()
	copy.Keys[0].Right = 99
	copy.LeftColumns[0] = 99
	sources := p.Sources()
	sources[0].Fields[0].Name = "modified"
	if p.Query().Keys[0].Left != 1 || p.Query().Keys[0].Right != 1 || p.Query().LeftColumns[0] != 1 || p.Sources()[0].Fields[0].Name == "modified" {
		t.Fatal("published relation exposes mutable configuration")
	}
}

// A dense independent oracle enumerates every possible pair. It does not use
// the production key builder, group walk, position order, or relation counters.
func TestRelationAllModesMatchIndependentDenseReference(t *testing.T) {
	random := rand.New(rand.NewSource(7213))
	key := func(value Value) string {
		if !value.Exists || value.Raw == "" || value.MissingCache || value.Type == "e" {
			return ""
		}
		if value.Type == "n" {
			n, ok := new(big.Rat).SetString(value.Raw)
			if !ok {
				return ""
			}
			return "number:" + n.RatString()
		}
		return "text:" + value.Raw
	}
	pool := []Value{text("a"), text("b"), text("001"), text("1"), number("1"), number("1.0"), text(""), {Type: "e", Raw: "#N/A", Exists: true}, {Type: "n", MissingCache: true, Exists: true}}
	for trial := 0; trial < 40; trial++ {
		var rows [2][][]Value
		for side := range rows {
			for i, n := 0, random.Intn(18); i < n; i++ {
				rows[side] = append(rows[side], []Value{pool[random.Intn(len(pool))], text(fmt.Sprintf("%d/%d", side, i))})
			}
		}
		p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), relationFixture(rows[0]...), relationFixture(rows[1]...), nil)
		if err != nil {
			t.Fatal(trial, err)
		}
		for _, mode := range []string{"left", "inner", "left_matched", "left_unmatched", "right_unmatched", "left_invalid", "right_invalid"} {
			want := []string{}
			for li, left := range rows[0] {
				k := key(left[0])
				matches := []int{}
				for ri, right := range rows[1] {
					if k != "" && k == key(right[0]) {
						matches = append(matches, ri)
					}
				}
				switch mode {
				case "left", "inner":
					for _, ri := range matches {
						want = append(want, fmt.Sprintf("%d/%d", li, ri))
					}
					if mode == "left" && len(matches) == 0 {
						want = append(want, fmt.Sprintf("%d/-1", li))
					}
				case "left_matched":
					if len(matches) > 0 {
						want = append(want, fmt.Sprintf("%d/-1", li))
					}
				case "left_unmatched":
					if k != "" && len(matches) == 0 {
						want = append(want, fmt.Sprintf("%d/-1", li))
					}
				case "left_invalid":
					if k == "" {
						want = append(want, fmt.Sprintf("%d/-1", li))
					}
				}
			}
			for ri, right := range rows[1] {
				k, matches := key(right[0]), false
				for _, left := range rows[0] {
					matches = matches || k != "" && k == key(left[0])
				}
				if mode == "right_invalid" && k == "" || mode == "right_unmatched" && k != "" && !matches {
					want = append(want, fmt.Sprintf("-1/%d", ri))
				}
			}
			got := []string{}
			for _, row := range relationCollect(t, p, mode) {
				l, r := -1, -1
				if row.Left != nil {
					l = row.Left.Position
				}
				if row.Right != nil {
					r = row.Right.Position
				}
				got = append(got, fmt.Sprintf("%d/%d", l, r))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("trial=%d mode=%s got=%v want=%v", trial, mode, got, want)
			}
			count, _ := p.Counts().Rows(mode)
			if count != int64(len(want)) {
				t.Fatal("preflight disagrees with dense reference", trial, mode, count, len(want))
			}
		}
		p.Close()
	}
}

func TestRelationCountsRemainExactAboveJavaScriptIntegerRange(t *testing.T) {
	c := RelationCounts{LeftJoinRows: 1_000_000_000_000_000_000, InnerRows: 999_999_999_999_999_999}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if json.Unmarshal(encoded, &decoded) != nil || decoded["leftJoinRows"] != "1000000000000000000" || decoded["innerRows"] != "999999999999999999" {
		t.Fatal(string(encoded))
	}
	if _, err := c.Rows("unknown"); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestRelationUncertainAndTruncatedKeysNeverBecomeConfirmedUnmatched(t *testing.T) {
	left := relationFixture([]Value{{Raw: "same", Display: "same", Type: "s", Exists: true, Truncated: true}, text("truncated")}, []Value{{Raw: "same", Display: "same", Type: "s", Exists: true, TypeMissing: true}, text("type missing")}, []Value{{Raw: "same", Display: "same", Type: "s", Exists: true, Uncertain: "unknown original state"}, text("unknown")})
	right := relationFixture([]Value{text("same"), text("target")})
	for _, mode := range []string{"", "text"} {
		q := relationQuery()
		q.Keys[0].Mode = mode
		p, err := PrepareRelation(context.Background(), t.TempDir(), q, left, right, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := int64(3)
		if mode == "text" {
			want = 2
		}
		if p.Counts().LeftInvalid != want || p.Counts().LeftOnly != 0 || p.Counts().InnerRows != 3-want {
			t.Fatal(mode, p.Counts())
		}
		p.Close()
	}
}

func TestRelationRejectsMalformedSourceAndQueryBeforeScanning(t *testing.T) {
	for _, kind := range []string{"no check", "negative rows", "excessive rows", "duplicate field", "duplicate output", "unknown output", "missing keys", "unknown key mode"} {
		t.Run(kind, func(t *testing.T) {
			left, right, q := relationFixture([]Value{text("one"), text("value")}), relationFixture(), relationQuery()
			scanned := false
			left.Scan = func(context.Context, []int, func(Record) error) error { scanned = true; return nil }
			switch kind {
			case "no check":
				left.Check = nil
			case "negative rows":
				left.SourceRows = -1
			case "excessive rows":
				left.SourceRows = formats.ExternalRows + 1
			case "duplicate field":
				left.Info.Fields = append(left.Info.Fields, left.Info.Fields[0])
			case "duplicate output":
				q.LeftColumns = []int{1, 1}
			case "unknown output":
				q.LeftColumns = []int{99}
			case "missing keys":
				q.Keys = nil
			case "unknown key mode":
				q.Keys[0].Mode = "guess"
			}
			if p, err := PrepareRelation(context.Background(), t.TempDir(), q, left, right, nil); err == nil || p != nil || scanned {
				t.Fatal(p, err, scanned)
			}
		})
	}
}

func TestRelationInflationBudgetBeforeAnyOutputAndConstantOpenReaders(t *testing.T) {
	var recordReads int
	makeSource := func(n int, reads *int) Source {
		rows := make([][]Value, n)
		for i := range rows {
			rows[i] = []Value{text("same"), text(fmt.Sprint(i))}
		}
		s := relationFixture(rows...)
		read := s.Read
		s.Read = func(ctx context.Context, positions iter.Seq[int], cols []int, visit func(Record) error) error {
			*reads++
			return read(ctx, positions, cols, func(row Record) error { recordReads++; return visit(row) })
		}
		return s
	}
	var reads int
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), makeSource(1000, &reads), makeSource(1000, &reads), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Counts().InnerRows != 1_000_000 || reads != 2 {
		t.Fatal(p.Counts(), reads)
	}
	emitted := false
	if n, err := p.Stream(context.Background(), "inner", 999_999, func(RelationRow) error { emitted = true; return nil }); err == nil || n != 0 || emitted || reads != 2 {
		t.Fatal("inflation was not refused before touching output", n, err, emitted, reads)
	}
	preview, err := p.Preview(context.Background(), "inner")
	if err != nil || len(preview.Rows) != 100 || !preview.Truncated || preview.Total != 1_000_000 || reads != 4 || recordReads != 2101 {
		t.Fatal("preview buffered a duplicate group or reread the unchanged left row", len(preview.Rows), err, reads, recordReads)
	}
}

func TestRelationSelectedValuesStayExactAndOversizedPreviewHasNoPartialRow(t *testing.T) {
	large := strings.Repeat("源", 700_000)
	left := relationFixture([]Value{text("key"), text(large)})
	right := relationFixture([]Value{text("key"), {Type: "n", MissingCache: true, Exists: true}})
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	preview, err := p.Preview(context.Background(), "inner")
	if err != nil || len(preview.Rows) != 0 || !preview.Truncated {
		t.Fatal(preview, err)
	}
	rows := relationCollect(t, p, "inner")
	if len(rows) != 1 || rows[0].Left.Values[2].Raw != large || !rows[0].Right.Values[2].MissingCache {
		t.Fatal("non-key source values or missing cache changed")
	}
}

func TestRelationCancellationFailuresAndEverySourceRevalidation(t *testing.T) {
	ctx := context.Background()
	left, right := relationFixture([]Value{text("left"), text("1")}), relationFixture([]Value{text("right"), text("2")})
	changed := false
	right.Check = func(ctx context.Context) error {
		if changed {
			return errors.New("other source changed")
		}
		return ctx.Err()
	}
	p, err := PrepareRelation(ctx, t.TempDir(), relationQuery(), left, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	changed = true
	if _, err := p.Preview(ctx, "left_unmatched"); err == nil {
		t.Fatal("unvisited right source was not checked")
	}
	changed = false
	cancelled, cancel := context.WithCancel(ctx)
	_, err = p.Stream(cancelled, "left", 1, func(RelationRow) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	sentinel := errors.New("consumer failed")
	_, err = p.Stream(ctx, "left", 1, func(RelationRow) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if len(relationCollect(t, p, "left")) != 1 {
		t.Fatal("failed extraction damaged the preflight")
	}
	var calls int
	left.Scan = func(ctx context.Context, cols []int, visit func(Record) error) error { calls++; return sentinel }
	if p, err := PrepareRelation(ctx, t.TempDir(), relationQuery(), left, right, nil); p != nil || !errors.Is(err, sentinel) || calls != 1 {
		t.Fatal(p, err, calls)
	}
}

func TestRelationSortReadMismatchAndIncompleteReadersFailClosed(t *testing.T) {
	for _, kind := range []string{"changed", "incomplete", "position"} {
		t.Run(kind, func(t *testing.T) {
			left := relationFixture([]Value{text("a"), text("1")}, []Value{text("b"), text("2")})
			read := left.Read
			left.Read = func(ctx context.Context, positions iter.Seq[int], cols []int, visit func(Record) error) error {
				if kind == "incomplete" {
					return nil
				}
				return read(ctx, positions, cols, func(row Record) error {
					if kind == "changed" {
						row.Values[1] = text("z")
					} else {
						row.Position++
					}
					return visit(row)
				})
			}
			if p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), left, relationFixture(), nil); err == nil || p != nil {
				t.Fatal("invalid preflight published", p, err)
			}
		})
	}
}

func TestRelationCloseDoesNotInvalidateBorrowedExtraction(t *testing.T) {
	left := relationFixture([]Value{text("a"), text("1")}, []Value{text("b"), text("2")})
	p, err := PrepareRelation(context.Background(), t.TempDir(), relationQuery(), left, relationFixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	entered, resume, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		var count int
		n, err := p.Stream(context.Background(), "left", 2, func(RelationRow) error {
			count++
			if count == 1 {
				close(entered)
				<-resume
			}
			return nil
		})
		if err == nil && n != 2 {
			err = errors.New("borrowed extraction lost records")
		}
		finished <- err
	}()
	<-entered
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Preview(context.Background(), "left"); err == nil {
		t.Fatal("released preflight admitted a new consumer")
	}
	close(resume)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
