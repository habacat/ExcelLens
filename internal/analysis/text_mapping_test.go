package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func mappingStep() Transform {
	return Transform{ID: "D_map", Name: "机构核对", Source: "C1", Op: "map_text", Mapping: []TextMapping{{From: "0001", To: "001"}, {From: "A", To: "B"}, {From: "B", To: "C"}}}
}

func TestTextMappingsAreExactNonCascadingAndRequireTrustedText(t *testing.T) {
	step := mappingStep()
	for _, tc := range []struct {
		value Value
		want  string
	}{
		{Value{Text: "A", Textual: true}, "B"},
		{Value{Text: "B", Textual: true}, "C"},
		{Value{Text: " A ", Textual: true}, " A "},
		{Value{Text: "a", Textual: true}, "a"},
		{Value{Text: "0001", Textual: true}, "001"},
		{Value{Text: "0001", Numeric: true}, "0001"},
		{Value{Text: "A"}, "A"},
		{Value{Text: "A", Textual: true, MissingCache: true}, "A"},
		{Value{Text: "A", Textual: true, ConversionFailed: true}, "A"},
	} {
		values := map[string]Value{"C1": tc.value}
		ApplyTransforms(values, []Transform{step})
		result := values[step.ID]
		if result.Text != tc.want || result.MissingCache != tc.value.MissingCache || result.ConversionFailed != tc.value.ConversionFailed || values["C1"] != tc.value {
			t.Fatal(tc, result, values["C1"])
		}
	}
	values := map[string]Value{"C1": {Text: "0001", Numeric: true}}
	after := CloneTransform(step)
	after.Source = "D_text"
	ApplyTransforms(values, []Transform{{ID: "D_text", Source: "C1", Op: "text"}, after})
	if values["D_map"].Text != "001" || values["D_map"].Numeric || !values["D_map"].Textual {
		t.Fatal("explicit text conversion was ignored", values)
	}
}

func TestTextMappingValidationRejectsHiddenDuplicateUnsortedOrOversizedRules(t *testing.T) {
	base := mappingStep()
	for _, change := range []func(*Transform){
		func(t *Transform) { t.Op = "replace" },
		func(t *Transform) { t.Arg = "ignored" },
		func(t *Transform) { t.Mapping = nil },
		func(t *Transform) { t.Mapping[1].From = "0001" },
		func(t *Transform) { t.Mapping[0], t.Mapping[1] = t.Mapping[1], t.Mapping[0] },
		func(t *Transform) { t.Mapping[0].From = "" },
		func(t *Transform) { t.Mapping[0].To = "\xff" },
		func(t *Transform) { t.Mapping[0].To = strings.Repeat("界", formats.TextMappingValueRunes+1) },
		func(t *Transform) { t.Mapping = make([]TextMapping, formats.TextMappingEntries+1) },
	} {
		step := CloneTransform(base)
		change(&step)
		if ValidateTextMapping(step) == nil {
			t.Fatal("invalid mapping accepted", step.Op)
		}
	}
	q := Query{Source: "wb:A", Aggregate: "count", Rows: []string{"D_map"}, Transforms: []Transform{base}}
	if err := Validate(q, []Field{{ID: "C1"}}); err != nil {
		t.Fatal(err)
	}
	rows := []Record{{Number: 1, Values: map[string]Value{"C1": {Text: "A", Textual: true}}}, {Number: 2, Values: map[string]Value{"C1": {Text: "B", Textual: true}}}}
	result, err := Run(context.Background(), q, []Field{{ID: "C1"}}, testScan(rows), nil)
	if err != nil || len(result.Rows) != 2 || result.Grand.Exact != "2" {
		t.Fatal(result, err)
	}
}

func TestTextMappingWorkspaceVersionFiveRejectsDowngradesAndPreservesOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.json")
	q := Query{Source: "wb:A", Aggregate: "count", Rows: []string{"D_map"}, Transforms: []Transform{mappingStep()}}
	w := EmptyWorkspace()
	w.Reports = []Report{{ID: "map", Name: "映射", Query: q}}
	if err := SaveWorkspace(path, w); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWorkspace(path)
	if err != nil || loaded.Version != 5 || !reflect.DeepEqual(loaded.Reports[0].Query.Transforms, q.Transforms) || ValidateWorkspace(loaded) != nil {
		t.Fatal(loaded, err)
	}
	for _, version := range []int{1, 2, 3, 4, 7} {
		bad := loaded
		bad.Version = version
		data, _ := json.Marshal(bad)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWorkspace(path); err == nil || ValidateWorkspace(bad) == nil {
			t.Fatal("legacy claim ignored mapping", version)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(after, data) {
			t.Fatal("rejected load rewrote original")
		}
	}
	loaded.Reports[0].Query.Transforms = nil
	loaded.Reports[0].Query.Rows = []string{"C1"}
	loaded.Reports[0].Query.Metrics = []Metric{{Aggregate: "count", Comparison: "percent_grand"}}
	if err := SaveWorkspace(path, loaded); err != nil {
		t.Fatal(err)
	}
	after, err := LoadWorkspace(path)
	if err != nil || after.Version != 5 {
		t.Fatal("comparison save downgraded mapping workspace", after, err)
	}
	old, _ := os.ReadFile(path)
	loaded.Reports[0].Query.Transforms = []Transform{mappingStep()}
	loaded.Reports[0].Query.Transforms[0].Mapping[0].From = "Z"
	if err := SaveWorkspace(path, loaded); err == nil {
		t.Fatal("invalid mapping persisted")
	}
	if data, _ := os.ReadFile(path); !bytes.Equal(data, old) {
		t.Fatal("invalid save replaced durable workspace")
	}
}
