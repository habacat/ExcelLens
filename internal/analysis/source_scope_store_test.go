package analysis

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/sourceview"
)

func TestScopedReportsUseWorkspaceVersionTwoWithoutReinterpretingLegacyFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.json")
	workspace := EmptyWorkspace()
	workspace.Reports = []Report{{ID: "scope", Name: "可见行", Query: Query{Source: "wb:A", SourceScope: &sourceview.Scope{Version: sourceview.ScopeVersion, Fingerprint: "source", Mode: "visible", Visibility: &sourceview.AxisViewSettings{Mode: "original"}}, Aggregate: "count"}}}
	if err := SaveWorkspace(path, workspace); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWorkspace(path)
	if err != nil || loaded.Version != 2 || loaded.Reports[0].Query.SourceScope == nil {
		t.Fatal("scoped report was not saved with a rejecting legacy-version boundary", loaded, err)
	}
	if err := ValidateWorkspace(loaded); err != nil {
		t.Fatal("backup validator rejected a valid scoped workspace", err)
	}
	for _, version := range []int{1, 2} {
		bad := loaded
		bad.Version = version
		if version == 2 {
			bad.Reports[0].Query.SourceScope.Version = 999
		}
		content, _ := json.Marshal(bad)
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWorkspace(path); err == nil {
			t.Fatal("invalid source scope silently became a complete source", version)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(after, content) {
			t.Fatal("failed loading rewrote scope evidence")
		}
	}
	legacy := EmptyWorkspace()
	legacy.Reports = []Report{{ID: "legacy", Name: "原始报表", Query: Query{Source: "wb:A", Aggregate: "count"}}}
	if err := SaveWorkspace(path, legacy); err != nil {
		t.Fatal(err)
	}
	if loaded, err = LoadWorkspace(path); err != nil || loaded.Version != 1 || loaded.Reports[0].Query.SourceScope != nil {
		t.Fatal("legacy complete-source report changed", loaded, err)
	}
}
