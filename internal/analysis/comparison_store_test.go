package analysis

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestComparisonReportsPersistAsVersionFourAndRejectLegacyVersionClaims(t *testing.T) {
	file := filepath.Join(t.TempDir(), "workspace.json")
	q := Query{Source: "wb:A", Rows: []string{"C1"}, Aggregate: "sum", Value: "C3", Metrics: []Metric{{Field: "C3", Aggregate: "sum", Comparison: "running_percent", ComparisonAxis: "columns"}}}
	workspace := EmptyWorkspace()
	workspace.Reports = []Report{{ID: "ratio", Name: "累计占比", Query: q}, {ID: "tree", Name: "层级", Query: Query{Source: "wb:A", Rows: []string{"C1"}, Layout: "tree"}}}
	if err := SaveWorkspace(file, workspace); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWorkspace(file)
	if err != nil || loaded.Version != 4 || !reflect.DeepEqual(loaded.Reports[0].Query.Metrics, q.Metrics) || ValidateWorkspace(loaded) != nil {
		t.Fatal(loaded, err)
	}
	for _, version := range []int{1, 2, 3, 7} {
		bad := loaded
		bad.Version = version
		data, _ := json.Marshal(bad)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWorkspace(file); err == nil || ValidateWorkspace(bad) == nil {
			t.Fatal("unsupported version silently discarded comparisons", version)
		}
		if after, _ := os.ReadFile(file); !bytes.Equal(data, after) {
			t.Fatal("failed read rewrote original workspace")
		}
	}
	loaded.Reports = loaded.Reports[1:]
	if err := SaveWorkspace(file, loaded); err != nil {
		t.Fatal(err)
	}
	if after, err := LoadWorkspace(file); err != nil || after.Version != 4 {
		t.Fatal("removing comparisons downgraded a newer workspace", after, err)
	}
	bad := loaded
	bad.Reports = []Report{{ID: "bad", Name: "bad", Query: q}}
	bad.Reports[0].Query.Metrics = []Metric{{Aggregate: "count", Comparison: "unsupported"}}
	before, _ := os.ReadFile(file)
	if err := SaveWorkspace(file, bad); err == nil || ValidateWorkspace(bad) == nil {
		t.Fatal("unknown comparison silently persisted")
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("invalid save changed durable workspace")
	}
}
