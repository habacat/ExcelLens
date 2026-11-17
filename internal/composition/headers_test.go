package composition

import "testing"

func TestAppendFieldSuggestionsRequireCompleteRawHeaderIdentityAndProvenUniqueness(t *testing.T) {
	field := func(column int, hash string) Field {
		return Field{Column: column, Name: "相同显示标签", HeaderHash: hash, HeaderLoaded: true, HeaderUnique: true}
	}
	expected := []Field{field(1, "exact-a"), field(2, "exact-b")}
	actual := []Field{field(9, "exact-b"), field(4, "exact-a")}
	matches := SuggestFields(expected, actual)
	if len(matches) != 2 || matches[0].Target != "C4" || matches[1].Target != "C9" {
		t.Fatal("unique header identity did not follow reordered source columns", matches)
	}
	for name, mutate := range map[string]func(*Field){
		"truncated":    func(f *Field) { f.Truncated = true },
		"not loaded":   func(f *Field) { f.HeaderLoaded = false },
		"not unique":   func(f *Field) { f.HeaderUnique = false },
		"hash missing": func(f *Field) { f.HeaderHash = "" },
		"display only": func(f *Field) { f.HeaderHash = "other exact header" },
	} {
		t.Run(name, func(t *testing.T) {
			target := field(4, "exact-a")
			mutate(&target)
			if match := SuggestFields(expected[:1], []Field{target})[0]; match.Target != "" || match.Status == "match" {
				t.Fatal("incomplete or same-label header auto-bound a source column", match)
			}
		})
	}
	for _, inputs := range [][2][]Field{
		{{field(1, "a"), field(2, "a")}, {field(4, "a")}},
		{{field(1, "a")}, {field(4, "a"), field(9, "a")}},
	} {
		for _, match := range SuggestFields(inputs[0], inputs[1]) {
			if match.Status != "ambiguous" || match.Target != "" {
				t.Fatal("duplicate raw headers were resolved by position", match)
			}
		}
	}
}
