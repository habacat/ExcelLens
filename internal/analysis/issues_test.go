package analysis

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestIssuesShareAggregationScopesAndPreservePhysicalProvenance(t *testing.T) {
	fields := []Field{{ID: "C1", Name: "源金额", Column: 1}, {ID: "C2", Name: "日期", Column: 2}, {ID: "C3", Name: "范围", Column: 3}}
	q := Query{Source: "test", Aggregate: "sum", Value: "C1", Metrics: []Metric{{Field: "C1", Aggregate: "sum"}, {Field: "C1", Aggregate: "average"}, {Field: "C1", Aggregate: "count"}}, Filters: []Filter{{Field: "C3", Op: "eq", Value: "keep"}},
		Transforms: []Transform{{ID: "D_trim", Name: "去空格", Source: "C2", Op: "trim"}, {ID: "D_date", Name: "转换日期", Source: "D_trim", Op: "date", Arg: "ymd"}}}
	rows := []Record{
		{Number: 7, Values: map[string]Value{"C1": {Text: "0001"}, "C2": {Text: " bad date "}, "C3": {Text: "keep"}}},
		{Number: 19, Values: map[string]Value{"C1": {MissingCache: true}, "C2": {Text: "2026-11-14"}, "C3": {Text: "keep"}}},
		{Number: 203, Values: map[string]Value{"C1": {Text: "bad numeric", Numeric: true}, "C2": {Text: "bad"}, "C3": {Text: "exclude"}}},
	}
	original := rows[0].Values["C2"]
	got, err := Run(context.Background(), q, fields, testScan(rows), nil)
	if err != nil {
		t.Fatal(err)
	}
	var extracted []Issue
	counts, err := VisitIssues(context.Background(), q, fields, testScan(rows), IssueSampleChars, func(issue Issue) error { extracted = append(extracted, issue); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.InvalidConversions != 2 || got.InvalidNumbers != 1 || got.MissingFormulaCaches != 1 || got.Included != 2 || got.Scanned != 3 {
		t.Fatalf("wrong scopes: %+v", got)
	}
	if counts.InvalidConversions != got.InvalidConversions || counts.InvalidNumbers != got.InvalidNumbers || counts.MissingFormulaCaches != got.MissingFormulaCaches || !reflect.DeepEqual(extracted, got.Issues) {
		t.Fatalf("extract diverged: %+v %+v", counts, got)
	}
	first := got.Issues[0]
	if first.Kind != "conversion" || first.Row != 7 || first.Field != "D_date" || first.SourceField != "C2" || first.SourceColumn != 2 || first.Original != " bad date " || first.Input != "bad date" {
		t.Fatal(first)
	}
	if rows[0].Values["C2"] != original {
		t.Fatal("diagnostics mutated source value")
	}
	if !strings.Contains(got.Issues[1].Reason, "文本") {
		t.Fatal("native text treated as a malformed source number", got.Issues[1])
	}
}

func TestIssueSamplesBoundTextByUnicodeButKeepExactCountsAndFullExportText(t *testing.T) {
	text := strings.Repeat("文😀", 3000)
	rows := make([]Record, 121)
	for i := range rows {
		rows[i] = Record{Number: i*3 + 2, Values: map[string]Value{"C1": {Text: text}}}
	}
	q := Query{Source: "test", Value: "C1", Aggregate: "sum"}
	fields := []Field{{ID: "C1", Name: "金额", Column: 1}}
	got, err := Run(context.Background(), q, fields, testScan(rows), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.InvalidNumbers != 121 || len(got.Issues) != MaxIssueSamples || !got.IssuesTruncated {
		t.Fatal(got.InvalidNumbers, len(got.Issues), got.IssuesTruncated)
	}
	for _, issue := range got.Issues {
		if !issue.TextTruncated || utf8.RuneCountInString(issue.Original) != IssueSampleChars || !utf8.ValidString(issue.Original) {
			t.Fatal("bad snippet", issue)
		}
	}
	count := 0
	_, err = VisitIssues(context.Background(), q, fields, testScan(rows), 0, func(issue Issue) error {
		count++
		if issue.Original != text || issue.TextTruncated {
			t.Fatal("full export text was shortened")
		}
		return nil
	})
	if err != nil || count != 121 {
		t.Fatal(count, err)
	}
	copy, truncated, err := CollectIssues(context.Background(), q, fields, testScan(rows), 2)
	if err != nil || len(copy) != 2 || !truncated || copy[0][0] != "2" || utf8.RuneCountInString(copy[0][5]) != MaxCellChars || copy[0][9] != "是" {
		t.Fatalf("bad bounded copy: %d %v %v", len(copy), truncated, err)
	}
	copy, truncated, err = CollectIssues(context.Background(), q, fields, testScan(rows[:2]), 2)
	if err != nil || len(copy) != 2 || truncated {
		t.Fatal("exactly full extraction incorrectly marked incomplete", err)
	}
}

func TestIssueCancellationAndVisitorFailurePropagate(t *testing.T) {
	q := Query{Source: "test", Value: "C1", Aggregate: "sum"}
	fields := []Field{{ID: "C1"}}
	rows := []Record{{Number: 9, Values: map[string]Value{"C1": {Text: "text"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VisitIssues(ctx, q, fields, testScan(rows), 0, func(Issue) error { t.Fatal("visited after cancellation"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	want := errors.New("disk full")
	if _, err := VisitIssues(context.Background(), q, fields, testScan(rows), 0, func(Issue) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
}
