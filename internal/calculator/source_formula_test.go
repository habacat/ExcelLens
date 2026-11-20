package calculator

import (
	"reflect"
	"testing"
)

func TestXLSXFormulaDependenciesAndDynamicScalarBoundary(t *testing.T) {
	for _, tc := range []struct {
		body   string
		sheets []string
		scalar bool
	}{
		{`SUMIFS('Data''s'!$C:$C,'Data''s'!$B:$B,_xlfn.SINGLE(B:B))/100`, []string{"Data's", "Data's"}, true},
		{`SUMIFS('Data''s'!$C:$C,'Data''s'!$B:$B,B:B)`, []string{"Data's", "Data's"}, false},
		{`"Other!A1"&$A$1`, nil, true},
		{`A1#`, nil, false},
		{`INDIRECT("A1")`, nil, true},
		{`INDIRECT("'Data''s'!B2:B3")`, []string{"Data's"}, false},
		{`SUM(_xlfn.ANCHORARRAY('Data''s'!B2))`, []string{"Data's"}, true},
		{`SUM('Data''s'!$2:$3)`, []string{"Data's"}, true},
	} {
		var sheets []string
		scalar, err := XLSXFormulaDependencies(t.Context(), tc.body, func(name string) bool {
			sheets = append(sheets, name)
			return name == "Data's"
		})
		if err != nil || scalar != tc.scalar || !reflect.DeepEqual(sheets, tc.sheets) {
			t.Errorf("%s: got %v, %t, %v", tc.body, sheets, scalar, err)
		}
	}
	for _, invalid := range []string{`INDIRECT(A1)`, "SUM(Name)", "SUM(Sheet1:Sheet2!A1)", "SUM('[external.xlsx]Sheet'!A1)", "SUM(XFE:XFE)", "A1:B3#"} {
		if _, err := XLSXFormulaDependencies(t.Context(), invalid, func(string) bool { return true }); err == nil {
			t.Errorf("unexpectedly accepted %s", invalid)
		}
	}
}
