package server

import (
	"encoding/json"
	"os"
	"testing"

	"excellens/internal/workbook"
)

func TestSearchCachePreservesAdvancedRegexCase(t *testing.T) {
	wb := &workbook.Workbook{CacheDir: t.TempDir()}
	upper, lower := `A regex "ABC"`, `A regex "abc"`
	job := &SearchJob{Status: "done", Sheet: "Data", Query: upper, Mode: "advanced", Scope: "current", Matches: []workbook.SearchResult{{Ref: "A1", Value: "ABC"}}}
	if err := saveSearchCache(wb, job); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSearchCache(wb, "Data", lower, "advanced", "current"); err == nil {
		t.Fatal("case-sensitive advanced query reused a different regex result")
	}
	if got, err := loadSearchCache(wb, "Data", upper, "advanced", "current"); err != nil || len(got.Matches) != 1 {
		t.Fatal(got, err)
	}
	if searchCacheKey("Data", `A regex "\D"`, "advanced", "current") == searchCacheKey("Data", `A regex "\d"`, "advanced", "current") {
		t.Fatal("opposite regex character classes collide")
	}
	// Old results cannot be trusted, including a same-spelling key that had
	// previously been overwritten by the other case variant.
	for _, version := range []int{1, 2} {
		old, _ := json.Marshal(persistedSearch{Version: version, Sheet: "Data", Query: lower, Mode: "advanced", Scope: "current"})
		if err := os.WriteFile(searchCachePath(wb, "Data", lower, "advanced", "current"), old, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSearchCache(wb, "Data", lower, "advanced", "current"); err == nil {
			t.Fatal("stale rendered-search cache accepted", version)
		}
	}
	if searchCachePath(wb, "Data", " SUM(A1) ", "formula", "current") != searchCachePath(wb, "Data", "sum(a1)", "formula", "current") {
		t.Fatal("ordinary formula searches no longer share case-insensitive cache")
	}
}
