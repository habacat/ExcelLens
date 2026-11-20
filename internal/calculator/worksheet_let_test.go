package calculator

import "testing"

func TestNestedLetStorageUsesOriginalBindingOffsets(t *testing.T) {
	const body = "LET(a,2,b,LET(a,5,a+1),a*b)"
	const want = "=_xlfn.LET(_xlpm.a,2,_xlpm.b,_xlfn.LET(_xlpm.a,5,_xlpm.a+1),_xlpm.a*_xlpm.b)"
	f, err := ParseLocalFormula(t.Context(), body, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.ArrayStorageFormula(); got != want {
		t.Fatalf("stored %s", got)
	}
	f, err = ParseLocalFormula(t.Context(), want[1:], nil, nil)
	if err != nil || f.ArrayStorageFormula() != want {
		t.Fatal("storage replay", err)
	}
}
