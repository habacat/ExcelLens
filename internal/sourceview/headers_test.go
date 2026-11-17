package sourceview

import "testing"

func TestHeaderMatchingCannotBindDuplicateFieldIDsThroughDifferentNames(t *testing.T) {
	expected := []HeaderIdentity{{ID: "C1", Hash: "amount", Complete: true, Unique: true}}
	actual := []HeaderIdentity{{ID: "C9", Hash: "amount", Complete: true, Unique: true}, {ID: "C9", Hash: "identifier", Complete: true, Unique: true}}
	match := MatchHeaders(expected, actual)[0]
	if match.Status != "ambiguous" || match.Target != "" {
		t.Fatal("a colliding field ID could resolve to a different typed field", match)
	}
}
