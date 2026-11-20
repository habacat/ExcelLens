package editfile

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestSheetViewEditsStopBeforeRowsAndPreserveTheirBytes(t *testing.T) {
	entries := strings.Repeat(`<row><c t="n"><v>17</v></c></row>`, 4000)
	const start = "\xef\xbb\xbf" + `<s:worksheet xmlns:s="` + mainNS + `"><s:dimension ref="A1"/>`
	for _, views := range []string{"", "<s:sheetViews/>", `<s:sheetViews><s:sheetView workbookViewId="0" tabSelected="1"/></s:sheetViews>`} {
		body := "<s:sheetData>" + entries + "</s:sheetData></s:worksheet>"
		raw := start + views + body
		r := strings.NewReader(raw)
		ps, err := sheetSelectionPatches(t.Context(), r, 1, []int{0, 1})
		if err != nil || len(ps) == 0 {
			t.Fatal(views, err)
		}
		if r.Len() < len(raw)/2 {
			t.Fatal("view scan consumed worksheet rows")
		}
		var out bytes.Buffer
		if err := writePartPatches(t.Context(), &out, strings.NewReader(raw), int64(len(raw)), ps); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), start) || !strings.HasSuffix(out.String(), body) || !strings.Contains(out.String(), `workbookViewId="1" tabSelected="1"`) || strings.Contains(out.String(), `workbookViewId="0" tabSelected="1"`) {
			t.Fatal("incorrect view splice")
		}
		ps, err = sheetSelectionPatches(t.Context(), io.Reader(&out), 1, []int{0, 1})
		if err != nil || len(ps) != 0 {
			t.Fatal("view edit is not idempotent", err)
		}
	}
}
