package clustering

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func recordsScan(records []Record) Scan {
	return func(ctx context.Context, visit func(Record) error) error {
		for _, record := range records {
			if err := visit(record); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
}

func TestUnicodeCandidateRulesAreExplicitAndDoNotGuessBusinessIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, left, right string
		options           Options
		equal             bool
	}{
		{"case default", "Acme", "acme", Options{}, false},
		{"case selected", "Acme", "acme", Options{FoldCase: true}, true},
		{"simple fold", "Kſ", "ks", Options{FoldCase: true}, true},
		{"full width", "ＡＢＣ１２", "ABC12", Options{FoldWidth: true}, true},
		{"width default", "ＡＢＣ", "ABC", Options{}, false},
		{"trim", "\u3000机构\t", "机构", Options{Space: "trim"}, true},
		{"collapse", "机构\t \u00a0上海  ", "机构 上海", Options{Space: "collapse"}, true},
		{"remove", "机构 上海", "机构上海", Options{Space: "remove"}, true},
		{"punctuation", "某（上海）有限公司", "某上海有限公司", Options{IgnorePunctuation: true}, true},
		{"order retained", "上海机构", "机构上海", Options{FoldCase: true, Space: "remove", IgnorePunctuation: true}, false},
		{"tokens retained", "A B", "B A", Options{FoldCase: true}, false},
		{"accent retained", "résumé", "resume", Options{FoldCase: true}, false},
		{"combining retained", "e\u0301", "é", Options{FoldCase: true}, false},
		{"symbols retained", "A+B", "AB", Options{IgnorePunctuation: true}, false},
		{"zeroes retained", "00012", "12", Options{FoldWidth: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := tc.options.Key(context.Background(), tc.left)
			if err != nil {
				t.Fatal(err)
			}
			b, err := tc.options.Key(context.Background(), tc.right)
			if err != nil || (a == b) != tc.equal {
				t.Fatal(a, b, err)
			}
		})
	}
}

func TestBuildPreservesRawTypesCountsFormulaEvidenceAndExclusions(t *testing.T) {
	cells := []workbook.CellData{
		{Raw: "客户（上海）", Type: "inlineStr"}, {Raw: "客户(上海)", Type: "s"},
		{Raw: "客户（上海）", Type: "str", HasFormula: true},
		{Raw: "0007", Type: "inlineStr"}, {Raw: "0007", Type: "inlineStr"},
		{Raw: "7", Type: "n"}, {Raw: "1,234.50", Type: "inlineStr"},
		{Raw: "TRUE", Type: "b"}, {Raw: "#N/A", Type: "e"},
		{Type: "str", HasFormula: true, CacheMissing: true}, {}, {Raw: "\u3000", Type: "s"}, {Raw: "孤立", Type: "s"},
	}
	var records []Record
	for i, cell := range cells {
		records = append(records, Record{Row: 21 + i, Cell: cell, Numeric: i == 6})
	}
	result, err := Build(context.Background(), t.TempDir(), len(records), recordsScan(records), Options{FoldWidth: true, Space: "collapse", IgnorePunctuation: true}, nil)
	want := Counts{Scanned: 13, Eligible: 6, Numeric: 1, Blank: 1, MissingCache: 1, OtherTypes: 3, EmptyKey: 1, Unique: 5, ExactGroups: 1, SimilarGroups: 1}
	if err != nil || result == nil || result.Counts != want || len(result.Groups) != 2 {
		t.Fatal(result, err)
	}
	found := false
	for _, group := range result.Groups {
		if group.Key != "客户上海" {
			if group.Key != "0007" || group.Count != 2 || group.Kind != "exact" {
				t.Fatal(group)
			}
			continue
		}
		found = true
		if group.Count != 3 || group.Kind != "similar" || len(group.Variants) != 3 {
			t.Fatal(group)
		}
		for _, v := range group.Variants {
			if v.Count != 1 || len(v.Rows) != 1 || (v.Type == "str" && (v.FormulaCount != 1 || v.Rows[0] != 23)) {
				t.Fatal("source evidence changed", v)
			}
		}
	}
	if !found || result.BudgetBytes() <= 0 || result.BudgetBytes() > formats.ClusterResultBytes {
		t.Fatal("missing group or budget", result)
	}
}

func TestArbitrarySourceSeparatorsDoNotCollideAndSamplesHaveFullCounts(t *testing.T) {
	var records []Record
	for i := 0; i < 27; i++ {
		records = append(records, Record{Row: 100 - i, Cell: workbook.CellData{Type: "s", Raw: "甲/\x00乙"}})
	}
	records = append(records, Record{Row: 200, Cell: workbook.CellData{Type: "s", Raw: "甲\x00/乙"}})
	result, err := Build(context.Background(), t.TempDir(), len(records), recordsScan(records), Options{}, nil)
	if err != nil || len(result.Groups) != 1 || result.Counts.Unique != 2 {
		t.Fatal(result, err)
	}
	v := result.Groups[0].Variants[0]
	if v.Count != 27 || v.Raw != "甲/\x00乙" || !reflect.DeepEqual(v.Rows, []int{74, 75, 76, 77, 78, 79, 80, 81}) {
		t.Fatal(v)
	}
}

func TestExternalSortCandidatesAreDeterministicAndCleanRuns(t *testing.T) {
	dir := t.TempDir()
	const rows = 12000
	scan := func(ctx context.Context, visit func(Record) error) error {
		for i := 0; i < rows; i++ {
			if err := visit(Record{Row: rows - i, Cell: workbook.CellData{Type: "inlineStr", Raw: fmt.Sprintf("000候选%04d", i%300)}}); err != nil {
				return err
			}
		}
		return nil
	}
	phases := 0
	result, err := build(context.Background(), dir, rows, scan, Options{}, func(stage string, _, _ int64) {
		if strings.Contains(stage, "归并") {
			phases++
		}
	}, limits{1000, 1000, 4 << 20, 64 << 10})
	if err != nil || len(result.Groups) != 300 || result.Counts.Eligible != rows || phases == 0 {
		t.Fatal(result, phases, err)
	}
	for i, g := range result.Groups {
		if g.ID != i || g.Count != 40 || g.Key != fmt.Sprintf("000候选%04d", i) || len(g.Variants) != 1 {
			t.Fatal(g)
		}
	}
	if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
		t.Fatal("completed sort retained temporary files", files, err)
	}
}

func TestCandidateFailuresNeverPublishPartialResults(t *testing.T) {
	records := []Record{{Row: 1, Cell: workbook.CellData{Raw: "a", Type: "s"}}, {Row: 2, Cell: workbook.CellData{Raw: "a", Type: "s"}}, {Row: 3, Cell: workbook.CellData{Raw: "b", Type: "s"}}, {Row: 4, Cell: workbook.CellData{Raw: "b", Type: "s"}}}
	for _, tc := range []struct {
		name  string
		total int
		scan  Scan
		limit limits
	}{
		{"group capacity", 4, recordsScan(records), limits{1, 100, 1 << 20, 64 << 10}},
		{"variant capacity", 4, recordsScan(records), limits{100, 1, 1 << 20, 64 << 10}},
		{"text capacity", 4, recordsScan(records), limits{100, 100, 600, 64 << 10}},
		{"short read", 5, recordsScan(records), limits{100, 100, 1 << 20, 64 << 10}},
		{"long read", 3, recordsScan(records), limits{100, 100, 1 << 20, 64 << 10}},
		{"reader error", 4, func(_ context.Context, v func(Record) error) error {
			_ = v(records[0])
			return errors.New("read failed")
		}, limits{100, 100, 1 << 20, 64 << 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if result, err := build(context.Background(), dir, tc.total, tc.scan, Options{}, nil, tc.limit); result != nil || err == nil {
				t.Fatal("partial result escaped", result, err)
			}
			if files, err := os.ReadDir(dir); err != nil || len(files) != 0 {
				t.Fatal("failed sort retained runs", files, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	scan := func(ctx context.Context, visit func(Record) error) error { cancel(); return visit(records[0]) }
	if result, err := Build(ctx, t.TempDir(), 4, scan, Options{}, nil); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatal(result, err)
	}
	for _, raw := range []string{strings.Repeat("x", formats.ClusterTextBytes+1), string([]byte{0xff})} {
		if key, err := (Options{}).Key(context.Background(), raw); err == nil || key != "" {
			t.Fatal("invalid/oversized source truncated", len(raw), err)
		}
	}
}

func FuzzCandidateKeysKeepUTF8AndRespectCancellation(f *testing.F) {
	for _, text := range []string{"机构（上海）", "Ａ\u3000b", "甲/\x00乙", "e\u0301", "\xff"} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 4096 {
			t.Skip()
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		options := Options{FoldCase: true, FoldWidth: true, Space: "collapse", IgnorePunctuation: true}
		if _, err := options.Key(ctx, text); err == nil {
			t.Fatal("cancelled normalization succeeded")
		}
		key, err := options.Key(context.Background(), text)
		if utf8.ValidString(text) && (err != nil || !utf8.ValidString(key)) {
			t.Fatal("valid UTF8 lost", err)
		}
		if !utf8.ValidString(text) && err == nil {
			t.Fatal("invalid source accepted")
		}
	})
}
