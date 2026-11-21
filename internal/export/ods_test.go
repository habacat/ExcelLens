package export

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func TestODSFilteredExportPreservesExplicitValues(t *testing.T) {
	root := exportTestStorage(t)
	source := filepath.Join(root, "source.ods")
	const content = `<o:document-content xmlns:o="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:t="urn:oasis:names:tc:opendocument:xmlns:table:1.0">
<o:body><o:spreadsheet><t:table t:name="Ledger">
<t:table-row-group t:display="false"><t:table-row t:number-rows-repeated="2"><t:table-cell o:value-type="string" o:string-value="1"/></t:table-row></t:table-row-group>
<t:table-row><t:table-cell o:value-type="string" o:string-value="001"/><t:table-cell o:value-type="date" o:date-value="2026-11-21"/>
<t:table-cell o:value-type="boolean" o:boolean-value="false"/><t:table-cell o:value-type="string" o:string-value=""/>
<t:table-cell o:value-type="boolean" t:formula="of:=TRUE()"/></t:table-row>
</t:table></o:spreadsheet></o:body></o:document-content>`
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, value := range map[string]string{"mimetype": "application/vnd.oasis.opendocument.spreadsheet", "content.xml": content} {
		part, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := workbook.OpenWorkbook(source)
	if err != nil {
		t.Fatal(err)
	}
	sheet := wb.Sheets[0]
	idx, err := wb.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := wb.SourceLayout(sheet, idx)
	if err != nil || len(layout.RowStates) != 1 || !layout.RowStates[0].Hidden || layout.RowStates[0].Min != 1 || layout.RowStates[0].Max != 2 {
		t.Fatal("hidden row group", layout.RowStates, err)
	}
	positions, err := wb.FilterSheet(idx, []workbook.FilterRule{{Column: 1, Operator: "equals", Value: "001"}}, nil, nil)
	if err != nil || len(positions) != 1 || idx.RowAt(positions[0]).Number != 3 {
		t.Fatal("filter lost the text ID or source coordinate", positions, err)
	}
	sel := Selection{Sheet: sheet, Index: idx, FirstRow: 1, LastRow: 3, FirstCol: 1, LastCol: 4, Compact: true, Filtered: true, RowNumbers: []int{idx.RowAt(positions[0]).Number}}
	_, rows := exportReopen(t, wb, sel, false, filepath.Join(root, "filtered.xlsx"))
	if len(rows) != 1 || len(rows[0].Cells) != 4 {
		t.Fatal("explicit empty string was lost", rows)
	}
	got := rows[0].Cells
	if got[0].Raw != "001" || got[0].Type != "inlineStr" || got[1].Raw != "2026-11-21" || got[1].Type != "d" || got[2].Type != "b" || got[3].Type != "inlineStr" || got[3].Raw != "" {
		t.Fatal(got)
	}
	sel.LastCol = 5
	for mode, want := range map[string]string{"formula": "OpenFormula", "values": "E3"} {
		err = Write(t.Context(), io.Discard, wb, []Selection{sel}, Options{Format: "xlsx", Mode: mode}, nil)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s export: %v", mode, err)
		}
	}
}
