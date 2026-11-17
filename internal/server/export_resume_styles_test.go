package server

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	excelExport "excellens/internal/export"
)

func exportResumeStyledWorkbook(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "two-sheets.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><workbookPr date1904="1"/><sheets><sheet name="First" sheetId="1" r:id="s1"/><sheet name="Other" sheetId="2" r:id="s2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="1"><numFmt numFmtId="164" formatCode="#,##0.00"/></numFmts><fonts count="1"><font><name val="Calibri"/><sz val="11"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="1"><xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs><dxfs count="2"><dxf><font><color rgb="FF2040FF"/><b/></font><fill><patternFill patternType="solid"><fgColor rgb="FFFFEEAA"/></patternFill></fill><numFmt numFmtId="165" formatCode="0.000"/></dxf><dxf><font><i/></font></dxf></dxfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1"><v>9007199254740993.31</v></c><c r="B1" t="inlineStr"><is><r><rPr><b/></rPr><t>001</t></r><r><rPr><i/></rPr><t>文本</t></r></is></c></row><row r="3"><c r="A3"><f>1+1</f><v>2</v></c></row></sheetData><conditionalFormatting sqref="A1 A3"><cfRule type="cellIs" operator="greaterThan" dxfId="0" priority="1"><formula>0</formula></cfRule></conditionalFormatting></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData><row r="1"><c r="A1"><v>1.005</v></c><c r="B1" t="inlineStr"><is><t>9007199254740993.31</t></is></c></row><row r="2"><c r="A2"><v>-2.50</v></c></row></sheetData><conditionalFormatting sqref="A1"><cfRule type="cellIs" operator="greaterThan" dxfId="0" priority="1"><formula>0</formula></cfRule></conditionalFormatting><conditionalFormatting sqref="A2"><cfRule type="cellIs" operator="lessThan" dxfId="1" priority="2"><formula>0</formula></cfRule></conditionalFormatting></worksheet>`,
	})
	return file
}

func TestExportResumeMultiSheetRestoresConditionalPaletteWithoutDuplicatingStyles(t *testing.T) {
	for _, format := range []string{"xlsx", "zipcsv"} {
		t.Run(format, func(t *testing.T) {
			isolateTestStorage(t)
			file := exportResumeStyledWorkbook(t)
			before := printScaleHash(t, file)
			a := checkpointOpen(t, file)
			wb := a.currentWorkbook()
			req := exportRequest{Scope: "all", Format: format, Mode: "display", Styles: true, IncludeHidden: true, BOM: true}
			selections := exportCheckpointPrepare(t, a, req)
			want := exportCheckpointOutput(t, wb, selections, req.options())
			store, lease := exportCheckpointCreate(t, a, req, selections)
			if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
				t.Fatal(err)
			}
			journal, err := newExportCheckpointJournal(context.Background(), wb, lease, selections)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			err = excelExport.Resume(ctx, wb, selections, req.options(), pausingExportJournal{ResumeJournal: journal, after: 1, pause: cancel}, nil)
			cancel()
			if !errors.Is(err, context.Canceled) || journal.State().Sheet != 1 || journal.State().Phase == "done" {
				t.Fatal("multi-sheet test did not stop after its first complete part", journal.State(), err)
			}
			m := lease.Snapshot()
			_ = lease.Close()
			_ = store.Close()
			a.Close()
			b := checkpointOpen(t, file)
			got := exportResumeDownload(t, b, exportResumeJob(t, b, m))
			compareResumedOutput(t, format, got, want, m.Created)
			if format == "xlsx" {
				parts := exportCheckpointParts(t, got)
				if !bytes.Contains([]byte(parts["xl/styles.xml"]), []byte(`cellXfs count="3"`)) || !bytes.Contains([]byte(parts["xl/workbook.xml"]), []byte(`date1904="1"`)) {
					t.Fatal("palette did not preserve source/derived style IDs and date system", parts["xl/styles.xml"])
				}
			}
			if printScaleHash(t, file) != before {
				t.Fatal("styled recovery rewrote its source")
			}
		})
	}
}
