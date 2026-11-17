package analysis

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/sourceview"
)

func TestTreeReportsRequireVersionThreeAndNeverDowngradeScopedWorkspaces(t *testing.T) {
	file := filepath.Join(t.TempDir(), "workspace.json")
	w := EmptyWorkspace()
	w.Reports = []Report{{ID: "tree", Name: "层级", Query: Query{Source: "wb:A", Rows: []string{"C1", "C2"}, Layout: "tree"}}, {ID: "scope", Name: "范围", Query: Query{Source: "wb:A", SourceScope: &sourceview.Scope{Version: 1, Fingerprint: "source", Mode: "visible", Visibility: &sourceview.AxisViewSettings{Mode: "original"}}}}}
	if err := SaveWorkspace(file, w); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWorkspace(file)
	if err != nil || loaded.Version != 3 || loaded.Reports[0].Query.Layout != "tree" || ValidateWorkspace(loaded) != nil {
		t.Fatal(loaded, err)
	}
	for _, version := range []int{1, 2, 999} {
		bad := loaded
		bad.Version = version
		encoded, _ := json.Marshal(bad)
		if err := os.WriteFile(file, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWorkspace(file); err == nil || ValidateWorkspace(bad) == nil {
			t.Fatal("tree was silently flattened", version)
		}
		after, _ := os.ReadFile(file)
		if !bytes.Equal(encoded, after) {
			t.Fatal("failed load rewrote the source configuration")
		}
	}
	loaded.Reports = loaded.Reports[1:]
	if err := SaveWorkspace(file, loaded); err != nil {
		t.Fatal(err)
	}
	if again, err := LoadWorkspace(file); err != nil || again.Version != 3 {
		t.Fatal("removing the last tree downgraded an existing workspace", again, err)
	}
}
