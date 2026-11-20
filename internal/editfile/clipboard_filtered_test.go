package editfile

import (
	"bytes"
	"testing"

	"excellens/internal/editdoc"
)

func TestFilteredSharedClipboardMembersUsePreFilterRows(t *testing.T) {
	p := xlsxPackage{layout: XLSXLayout{Sheets: []XLSXSheet{{Name: "Data"}}}}
	raw := []byte(`<worksheet xmlns="` + mainNS + `"><sheetData><row r="2"><c r="D2"><f t="shared" si="0" ref="D2:D10">C2+$B$1</f></c></row><row r="3"><c r="D3"><f t="shared" si="0"/></c></row></sheetData></worksheet>`)
	next, err := p.expandMappedGroups(t.Context(), raw, "Data", axisEdit{Structure: editdoc.Structure{Sheet: "Data"}}, func(row int) int {
		if row == 3 {
			return 10
		}
		return row
	})
	if err != nil || !bytes.Contains(next, []byte(">C10+$B$1</f>")) {
		t.Fatalf("%s %v", next, err)
	}
}

func TestVisibleClipboardRowsKeepTextFormulasAndRefuseSplitMerges(t *testing.T) {
	clip := &editdoc.PortableRange{Start: editdoc.Address{Sheet: "Data", Row: 6, Col: 1}, Rows: 4, Cols: 1, VisibleSourceRows: []int{7, 9}, Cells: make([]editdoc.PortableCell, 4)}
	clip.Cells[1].Value = editdoc.Value{Kind: "text", Raw: "00123"}
	clip.Cells[3].Value = editdoc.Value{Kind: "formula", Raw: "=B9"}
	got, err := visibleClipboardRange(clip)
	if err != nil || len(got.Cells) != 2 || got.Start.Row != 7 || got.Cells[1].Value.Raw != "=B9" || len(clip.Cells) != 4 {
		t.Fatal(got, err)
	}
	clip.Merges = []editdoc.Area{{Start: editdoc.Address{Sheet: "Data", Row: 1, Col: 1}, EndRow: 3, EndCol: 1}}
	if _, err := visibleClipboardRange(clip); err == nil {
		t.Fatal("partially visible merge accepted")
	}
}

func TestShadowSelectionDistinguishesHiddenColumnsWithEmptyFormulaCaches(t *testing.T) {
	number := func(raw string) editdoc.Value { return editdoc.Value{Kind: "number", Raw: raw} }
	r := &editdoc.PortableRange{Start: editdoc.Address{Sheet: "Data", Row: 7, Col: 4}, Rows: 1, Cols: 3,
		Cells:             []editdoc.PortableCell{{Value: number("17")}, {Value: number("24")}, {Value: editdoc.Value{Kind: "formula", Raw: "=E7+1"}}},
		VisibleSourceCols: []int{4, 6}}
	v, err := visibleClipboardRange(r)
	if err != nil {
		t.Fatal(err)
	}
	cells := map[[2]int]editdoc.Value{{1, 1}: number("17.0"), {1, 2}: number("25")}
	if !matchesShadow(v, cells) || matchesShadow(r, cells) || v.SourceCols[1] != 6 {
		t.Fatal("formula column lost its source position")
	}
	cells[[2]int{1, 2}] = number("24")
	if !matchesShadow(r, cells) || !matchesShadow(v, cells) {
		t.Fatal("an ambiguous snapshot must retain both choices")
	}
}
