package analysis

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/sourceview"
)

func compositionQueryFixture() Query {
	definition := sourceview.Append{Version: 1, Name: "联合", Fields: []string{"金额"}, Inputs: []sourceview.AppendInput{
		{Ref: sourceview.TableRef{Book: "book-a", Fingerprint: "a", Source: "wb:A", Stamp: "a", HeaderRow: 1}, Columns: []int{2}},
		{Ref: sourceview.TableRef{Book: "book-b", Fingerprint: "b", Source: "wb:B", Stamp: "b", HeaderRow: 1}, Columns: []int{1}},
	}}
	return Query{Source: "append:" + definition.Digest(), Composition: &definition, Aggregate: "sum", Value: "C1"}
}

func TestCompositionReportsRequireWorkspaceSixAndPreserveDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.json")
	ws := EmptyWorkspace()
	ws.Reports = []Report{{ID: "union", Name: "联合金额", Query: compositionQueryFixture()}}
	if err := SaveWorkspace(path, ws); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWorkspace(path)
	if err != nil || loaded.Version != 6 || loaded.Reports[0].Query.Composition.Digest() != ws.Reports[0].Query.Composition.Digest() {
		t.Fatal("union report lost its descriptor or old-reader rejection boundary", loaded, err)
	}
	if err := ValidateWorkspace(loaded); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2, 3, 4, 5, 7} {
		bad := loaded
		bad.Version = version
		data, _ := json.Marshal(bad)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWorkspace(path); err == nil || ValidateWorkspace(bad) == nil {
			t.Fatal("invalid version accepted append semantics", version)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(after, data) {
			t.Fatal("rejected descriptor was overwritten")
		}
	}
	loaded.Reports = nil
	if err := SaveWorkspace(path, loaded); err != nil {
		t.Fatal(err)
	}
	if saved, err := LoadWorkspace(path); err != nil || saved.Version != 6 {
		t.Fatal("workspace silently downgraded", saved, err)
	}
}

func TestCompositionQueryRejectsMissingChangedAndConflictingDescriptors(t *testing.T) {
	fields := []Field{{ID: "C1", Name: "金额", Column: 1}}
	if err := Validate(compositionQueryFixture(), fields); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Query){
		"missing":         func(q *Query) { q.Composition = nil },
		"changed mapping": func(q *Query) { q.Composition.Inputs[0].Columns[0] = 3 },
		"changed name":    func(q *Query) { q.Composition.Name = "另一联合" },
		"single source":   func(q *Query) { q.Source = "wb:A" },
		"header":          func(q *Query) { q.HeaderRow = 1 },
		"scope":           func(q *Query) { q.SourceScope = &sourceview.Scope{} },
		"version":         func(q *Query) { q.Composition.Version++ },
	} {
		t.Run(name, func(t *testing.T) {
			q := compositionQueryFixture()
			mutate(&q)
			if Validate(q, fields) == nil {
				t.Fatal("conflicting descriptor accepted")
			}
		})
	}
	q := compositionQueryFixture()
	data, _ := json.Marshal(q)
	data = bytes.Replace(data, []byte(`"composition":{`), []byte(`"composition":{"unknown":true,`), 1)
	if json.Unmarshal(data, &q) == nil {
		t.Fatal("unknown append definition field ignored")
	}
}
