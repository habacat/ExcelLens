package sourceview

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func appendFixture() Append {
	return Append{Version: AppendVersion, Name: "两表追加", Fields: []string{"编号", "金额", "备注"}, Inputs: []AppendInput{
		{Ref: TableRef{Book: "path-a", Fingerprint: "same-content", Source: "wb:甲表", Stamp: "stamp-a", HeaderRow: 1}, Columns: []int{1, 2, 0}},
		{Ref: TableRef{Book: "path-b", Fingerprint: "same-content", Source: "wb:甲表", Stamp: "stamp-b", HeaderRow: 1}, Columns: []int{4, 0, 2}},
	}}
}

func TestAppendDescriptorKeepsMissingColumnsOrderPathIdentityAndOwnedScopes(t *testing.T) {
	input := appendFixture()
	input.Inputs[0].Ref.Scope = &Scope{Version: ScopeVersion, Fingerprint: "same-content", Mode: "selection", Selection: &Selection{StartRow: 2, EndRow: 9, StartColumn: 1, EndColumn: 2}}
	normalized, err := input.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	digest := normalized.Digest()
	input.Fields[0] = "改名"
	input.Inputs[0].Columns[0] = 8
	input.Inputs[0].Ref.Scope.Selection.StartRow = 8
	if normalized.Fields[0] != "编号" || normalized.Inputs[0].Columns[0] != 1 || normalized.Inputs[0].Ref.Scope.Selection.StartRow != 2 || normalized.Digest() != digest {
		t.Fatal("normalized configuration retained mutable caller input", normalized)
	}
	encoded, _ := json.Marshal(normalized)
	var restored Append
	if err := json.Unmarshal(encoded, &restored); err != nil || !reflect.DeepEqual(restored, normalized) || restored.Digest() != digest {
		t.Fatal("durable round trip lost source or mapping", restored, err)
	}
	for _, mutate := range []func(*Append){
		func(a *Append) { a.Inputs[0], a.Inputs[1] = a.Inputs[1], a.Inputs[0] },
		func(a *Append) {
			a.Inputs[1].Columns[0], a.Inputs[1].Columns[2] = a.Inputs[1].Columns[2], a.Inputs[1].Columns[0]
		},
		func(a *Append) { a.Inputs[0].Ref.Stamp = "new-stamp" },
	} {
		changed, _ := normalized.Normalized()
		mutate(&changed)
		if changed.Digest() == digest {
			t.Fatal("source order, mappings or source version were omitted from identity")
		}
	}
}

func TestAppendDescriptorRejectsAmbiguityInvalidBindingsAndUnboundedInput(t *testing.T) {
	for name, mutate := range map[string]func(*Append){
		"old version":        func(a *Append) { a.Version = 0 },
		"future version":     func(a *Append) { a.Version++ },
		"blank name":         func(a *Append) { a.Name = " " },
		"long name":          func(a *Append) { a.Name = strings.Repeat("甲", 81) },
		"duplicate output":   func(a *Append) { a.Fields[1] = a.Fields[0] },
		"blank output":       func(a *Append) { a.Fields[1] = "" },
		"missing binding":    func(a *Append) { a.Inputs[0].Columns = []int{1} },
		"negative binding":   func(a *Append) { a.Inputs[0].Columns[0] = -1 },
		"too wide binding":   func(a *Append) { a.Inputs[0].Columns[0] = formats.ExcelColumns + 1 },
		"duplicate binding":  func(a *Append) { a.Inputs[0].Columns[0] = 2 },
		"empty source":       func(a *Append) { a.Inputs[0].Columns = []int{0, 0, 0} },
		"empty output":       func(a *Append) { a.Inputs[0].Columns[1] = 0 },
		"nested composition": func(a *Append) { a.Inputs[0].Ref.Source = "append:other" },
		"path instead of id": func(a *Append) { a.Inputs[0].Ref.Source = "D:\\private.xlsx" },
		"missing fingerprint": func(a *Append) {
			a.Inputs[0].Ref.Fingerprint = ""
		},
		"duplicate source different stamp": func(a *Append) {
			a.Inputs[1].Ref = a.Inputs[0].Ref
			a.Inputs[1].Ref.Stamp = "pretend-changed"
		},
		"foreign scope": func(a *Append) {
			a.Inputs[0].Ref.Scope = &Scope{Version: ScopeVersion, Fingerprint: "other", Mode: "selection", Selection: &Selection{StartRow: 2, EndRow: 3, StartColumn: 1, EndColumn: 2}}
		},
		"scratch header": func(a *Append) { a.Inputs[0].Ref.Source = "scratch:one" },
		"too many sources": func(a *Append) {
			for len(a.Inputs) <= formats.CompositionSources {
				a.Inputs = append(a.Inputs, a.Inputs[0])
			}
		},
		"too many fields": func(a *Append) { a.Fields = make([]string, formats.CompositionColumns+1) },
	} {
		t.Run(name, func(t *testing.T) {
			input := appendFixture()
			mutate(&input)
			if _, err := input.Normalized(); err == nil {
				t.Fatal("invalid or ambiguous append accepted", input)
			}
		})
	}
	for _, raw := range []string{
		`{"version":1,"unknown":true}`,
		`{"inputs":[{"ref":{"book":"a","unknown":true}}]}`,
		`{} {}`,
		strings.Repeat(" ", formats.CompositionRequestBytes+1),
	} {
		var input Append
		if err := json.Unmarshal([]byte(raw), &input); err == nil {
			t.Fatal("unknown structure, trailing input or oversized configuration accepted")
		}
	}
}
