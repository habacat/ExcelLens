package server

import "testing"

func TestSourceHeadersStayDistinctWhenOriginalMatchesGeneratedSuffix(t *testing.T) {
	used := map[string]bool{}
	for i, header := range []string{"金额", "金额 · C", "金额", "E 列", ""} {
		got := workbookAnalysisField(i+1, header, used)
		want := []string{"金额", "金额 · C", "金额 · C (2)", "E 列", "E 列 · E"}[i]
		if got.Name != want || got.Column != i+1 {
			t.Fatalf("column %d: %+v", i+1, got)
		}
	}
}
