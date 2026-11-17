package sourceview

import (
	"encoding/json"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func TestScopeKeepsSourceAndVisualEndpointsAndCopiesMutableRuleInputs(t *testing.T) {
	depth := uint8(2)
	original := Scope{Version: ScopeVersion, Fingerprint: "source", Mode: "selection", FrozenThrough: 3,
		Filter:     &FilterSpec{HeaderRow: 1, Rules: []FilterRule{{Column: 2, Operator: "in", Values: []string{"0001", "0.10"}}}, SortKeys: []SortKey{{Column: 3, Descending: true, Mode: "number"}}},
		Visibility: &AxisViewSettings{Depth: &depth, Ranges: []AxisVisibilityOverride{{Min: 4, Max: 6, Hidden: true}}, Groups: []AxisOutlineOverride{{AxisOutlineKey: AxisOutlineKey{Min: 10, Max: 12, Level: 1}, Collapsed: true}}},
		Selection:  &Selection{StartRow: 2, EndRow: 97, StartColumn: 2, EndColumn: 7, VisualStart: 2, VisualEnd: 1}}
	clean, err := original.Normalized(100, 12)
	if err != nil {
		t.Fatal(err)
	}
	before := original.Digest()
	clean.Filter.Rules[0].Values[0] = "changed"
	clean.Filter.SortKeys[0].Column = 4
	clean.Visibility.Ranges[0].Min = 5
	clean.Visibility.Groups[0].Collapsed = false
	*clean.Visibility.Depth = 7
	clean.Selection.EndRow = 96
	if original.Digest() != before || original.Selection.EndRow != 97 || depth != 2 {
		t.Fatal("normalization aliased captured source settings")
	}
	if clean.Digest() == before {
		t.Fatal("range/content changes did not distinguish the source stamp")
	}
}

func TestScopeRejectsUnknownOrWidenedDescriptions(t *testing.T) {
	base := Scope{Version: ScopeVersion, Fingerprint: "source", Mode: "visible", Visibility: &AxisViewSettings{Mode: "original"}}
	for _, change := range []func(*Scope){
		func(s *Scope) { s.Version = 2 }, func(s *Scope) { s.Mode = "all" }, func(s *Scope) { s.Fingerprint = "" }, func(s *Scope) { s.Visibility = nil }, func(s *Scope) { s.FrozenThrough = 31 },
		func(s *Scope) { s.Mode = "filtered" }, func(s *Scope) {
			s.Mode = "selection"
			s.Selection = &Selection{StartRow: 1, EndRow: 101, StartColumn: 1, EndColumn: 2}
		},
		func(s *Scope) {
			s.Mode = "selection"
			s.Selection = &Selection{StartRow: 1, EndRow: 10, StartColumn: 1, EndColumn: 2, VisualStart: 1}
		},
		func(s *Scope) { s.Filter = &FilterSpec{Rules: []FilterRule{{Column: 20, Operator: "blank"}}} },
	} {
		candidate := base
		change(&candidate)
		if _, err := candidate.Normalized(100, 12); err == nil {
			t.Fatalf("invalid scope accepted: %+v", candidate)
		}
	}
	tooLarge := base
	tooLarge.Filter = &FilterSpec{Rules: []FilterRule{{Column: 1, Operator: "equal", Value: strings.Repeat("x", formats.SourceScopeBytes+1)}}}
	if err := tooLarge.Validate(); err == nil {
		t.Fatal("oversized durable source rules accepted")
	}
	for _, data := range []string{`{"version":1,"fingerprint":"source","mode":"visible","visibility":{"mode":"original","unrecognized":true}}`, `{"version":1,"fingerprint":"source","mode":"filtered","filter":{"rules":[{"column":1,"operator":"equal","value":"x","unknown":true}]}}`} {
		var scope Scope
		if json.Unmarshal([]byte(data), &scope) == nil {
			t.Fatal("unknown nested scope fields were silently ignored", data)
		}
	}
}
