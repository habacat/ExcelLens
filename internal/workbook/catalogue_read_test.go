package workbook

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/formats"
)

const catalogueTestNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
const catalogueTestRel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

func catalogueSourceParts() map[string]string {
	return map[string]string{
		"xl/workbook.xml":                     `<workbook xmlns="` + catalogueTestNS + `" xmlns:r="` + catalogueTestRel + `"><sheets><sheet name="图表页" sheetId="1" r:id="s0"/><sheet name="原表" sheetId="2" r:id="s1"/><sheet name="隐藏表" sheetId="3" state="hidden" r:id="s2"/></sheets><definedNames><definedName name="金额" comment="源注释"> '原表'!$B$2:$B$9 </definedName><definedName name="局部" localSheetId="1" hidden="true">$D$9</definedName><definedName name="结构化">账表[金额]</definedName></definedNames></workbook>`,
		"xl/_rels/workbook.xml.rels":          `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s0" Type="` + catalogueTestRel + `/chartsheet" Target="chartsheets/sheet0.xml"/><Relationship Id="s1" Type="` + catalogueTestRel + `/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="` + catalogueTestRel + `/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":            `<worksheet xmlns="` + catalogueTestNS + `" xmlns:r="` + catalogueTestRel + `"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>0001</t></is></c><c r="B1"><v>9007199254741015.11</v></c></row></sheetData><tableParts count="1"><tablePart r:id="t1"/></tableParts></worksheet>`,
		"xl/worksheets/sheet2.xml":            `<worksheet xmlns="` + catalogueTestNS + `"><sheetData/></worksheet>`,
		"xl/worksheets/_rels/sheet1.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships" xmlns:e="urn:extension"><Relationship Id="t1" Type="` + catalogueTestRel + `/table" Target="../tables/table%201.xml"/><Relationship Id="tOrphan" Type="` + catalogueTestRel + `/table" Target="/xl/tables/orphan.xml"/><Relationship Id="tExternal" Type="` + catalogueTestRel + `/table" TargetMode="External" Target="https://example.invalid/table.xml"/><e:Relationship Id="fake" Type="` + catalogueTestRel + `/table" Target="../tables/fake.xml"/></Relationships>`,
		"xl/tables/table 1.xml":               `<table xmlns="` + catalogueTestNS + `" id="1" name="账表" displayName="账表" ref="B3:D9" headerRowCount="1" totalsRowCount="1"><tableColumns count="3"><tableColumn id="20" name="编号"/><tableColumn id="5" name="金额"/><tableColumn id="99" name="备注"/></tableColumns></table>`,
		"xl/tables/orphan.xml":                `<table xmlns="` + catalogueTestNS + `" id="2" name="孤立关系" displayName="孤立关系" ref="F3:F9"><tableColumns count="1"><tableColumn id="1" name="编号"/></tableColumns></table>`,
	}
}

func writeCatalogueZIPFixture(t *testing.T, parts map[string]string, unreadableBodies bool) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "catalogue.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, content := range parts {
		var writer io.Writer
		if unreadableBodies && strings.HasPrefix(name, "xl/worksheets/") && strings.HasSuffix(name, ".xml") {
			// Deliberately unreadable and declared huge. Metadata listing must
			// succeed without calling Open on either worksheet body.
			writer, err = z.CreateRaw(&zip.FileHeader{Name: name, Method: 99, CompressedSize64: 1, UncompressedSize64: 8 << 30})
			content = "x"
		} else {
			writer, err = z.Create(name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestCatalogueReadsOnlyMetadataFromRealZIPAndLeavesTableCandidatesUnverified(t *testing.T) {
	isolateTestStorage(t)
	file := writeCatalogueZIPFixture(t, catalogueSourceParts(), true)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	result, err := wb.ReadCatalogue(context.Background())
	if err != nil || !result.Complete || len(result.Items) != 6 {
		t.Fatal("metadata listing opened worksheet or missed source entries", result, err)
	}
	global := catalogueFind(t, result, "金额", "")
	local := catalogueFind(t, result, "局部", "原表")
	if global.Status != "ready" || global.Definition != " '原表'!$B$2:$B$9 " || global.Comment != "源注释" || global.Areas[0].Start != "B2" || local.Status != "ready" || local.ScopeID != "1" || !local.Hidden || local.Areas[0].Start != "D9" {
		t.Fatal(global, local)
	}
	for _, item := range result.Items[2:] {
		if item.Status == "ready" || len(item.Areas) > 0 || item.Reason == "" {
			t.Fatal("relationship alone verified a table or a name that refers to it", item)
		}
	}
	table := catalogueFind(t, result, "账表", "")
	if table.Sheet != "原表" || table.Table.Part != "xl/tables/table 1.xml" || table.Table.RelationID != "t1" || table.Table.Verified || len(table.Table.Columns) != 3 {
		t.Fatal(table)
	}
	// Mutation by a caller must not become a cached source declaration.
	table.Table.Verified = true
	table.Table.Columns[0].Name = "用户改动"
	second, err := wb.ReadCatalogue(context.Background())
	if err != nil || catalogueFind(t, second, "账表", "").Table.Verified || catalogueFind(t, second, "账表", "").Table.Columns[0].Name != "编号" {
		t.Fatal("source catalogue shared caller-owned state", second, err)
	}
	for _, sheet := range wb.Sheets {
		if wb.GetCachedIndex(sheet.Path) != nil {
			t.Fatal("listing built a worksheet index", sheet)
		}
		xmlPath, indexPath := wb.cachePaths(sheet.Path)
		for _, path := range []string{xmlPath, indexPath} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("listing decompressed or indexed a worksheet", path, err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if cancelled, err := wb.ReadCatalogue(ctx); !errors.Is(err, context.Canceled) || len(cancelled.Items) != 0 {
		t.Fatal("cancelled listing returned partial data", cancelled, err)
	}
	after, err := os.ReadFile(file)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source changed", err)
	}
}

func TestCatalogueReadSourceChangeAndMalformedOrOverBudgetPartsRemainExplicit(t *testing.T) {
	isolateTestStorage(t)
	for _, mode := range []string{"oversize", "missing", "badXML", "duplicateRelation", "changedSource"} {
		t.Run(mode, func(t *testing.T) {
			parts := catalogueSourceParts()
			switch mode {
			case "oversize":
				parts["xl/tables/table 1.xml"] += strings.Repeat(" ", formats.CataloguePartBytes)
			case "missing":
				delete(parts, "xl/tables/table 1.xml")
			case "badXML":
				parts["xl/tables/table 1.xml"] += "broken tail"
			case "duplicateRelation":
				parts["xl/worksheets/_rels/sheet1.xml.rels"] = strings.Replace(parts["xl/worksheets/_rels/sheet1.xml.rels"], `Id="tOrphan"`, `Id="t1"`, 1)
			}
			file := writeCatalogueZIPFixture(t, parts, false)
			wb, err := OpenWorkbook(file)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changedSource" {
				replacement := catalogueSourceParts()
				replacement["xl/workbook.xml"] = strings.ReplaceAll(replacement["xl/workbook.xml"], "$B$2", "$C$2")
				newFile := writeCatalogueZIPFixture(t, replacement, false)
				data, err := os.ReadFile(newFile)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := wb.ReadCatalogue(context.Background())
			if mode == "changedSource" {
				if err == nil || len(result.Items) != 0 {
					t.Fatal("old workbook published changed-source metadata", result, err)
				}
				return
			}
			if err != nil || result.Complete || len(result.Notices) == 0 {
				t.Fatal("partial metadata hidden", result, err)
			}
			if mode != "duplicateRelation" {
				found := false
				for _, item := range result.Items {
					if item.Table != nil && item.Table.RelationID == "t1" {
						found = true
						if item.Status == "ready" || item.Status == "unresolved" || item.Reason == "" || item.Table.Verified {
							t.Fatal("damaged table looked valid", item)
						}
					}
				}
				if !found {
					t.Fatal("failed table relationship disappeared from directory", result)
				}
			}
		})
	}
}

func TestCataloguePartReaderChecksTotalBudgetCRCAndCancellation(t *testing.T) {
	isolateTestStorage(t)
	file := writeCatalogueZIPFixture(t, catalogueSourceParts(), false)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := wb.openCatalogueReader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.zip.Close()
	reader.bytes = formats.CatalogueBytes - 10
	if _, err := reader.part("xl/workbook.xml"); err == nil || reader.bytes != formats.CatalogueBytes-10 {
		t.Fatal("total budget exceeded before opening part", reader.bytes, err)
	}
	reader.bytes = formats.CatalogueBytes + 1
	if _, err := reader.part("xl/workbook.xml"); err == nil {
		t.Fatal("exhausted budget wrapped into a larger allowance")
	}
	reader.bytes = 0
	reader.entries["xl/workbook.xml"].CRC32 ^= 1
	if _, err := reader.part("xl/workbook.xml"); !errors.Is(err, zip.ErrChecksum) {
		t.Fatal("metadata CRC error ignored", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var destination bytes.Buffer
	if _, err := io.Copy(&destination, catalogueContextReader{ctx, strings.NewReader("not read")}); !errors.Is(err, context.Canceled) || destination.Len() != 0 {
		t.Fatal("cancelled metadata read continued", err, destination.String())
	}
}

func TestCatalogueNonMetadataSourcesDoNotGetInterpretedAsOOXML(t *testing.T) {
	for _, format := range []string{"csv", "tsv", "txt", "sqlite", "xls-html"} {
		book := &Workbook{Format: format, Path: filepath.Join(t.TempDir(), "absent"), source: &externalSource{}}
		result, err := book.ReadCatalogue(context.Background())
		if err != nil || !result.Complete || len(result.Items) != 0 {
			t.Fatal("non-metadata adapter tried to read OOXML or BIFF records", format, result, err)
		}
	}
}

func TestCatalogueHyperlinkScopesUseSourceSheetsRatherThanExtensionLookalikes(t *testing.T) {
	isolateTestStorage(t)
	parts := catalogueSourceParts()
	parts["xl/workbook.xml"] = strings.Replace(parts["xl/workbook.xml"], "<workbook ", `<workbook xmlns:e="urn:extension" `, 1)
	parts["xl/workbook.xml"] = strings.Replace(parts["xl/workbook.xml"], `<sheet name="原表"`, `<e:sheet name="扩展伪工作表" r:id="fake"/><sheet name="原表"`, 1)
	wb, err := OpenWorkbook(writeCatalogueZIPFixture(t, parts, false))
	if err != nil {
		t.Fatal(err)
	}
	if wb.LocalNamedRanges["原表"]["局部"] != "$D$9" || len(wb.LocalNamedRanges["扩展伪工作表"]) != 0 {
		t.Fatal("extension shifted hyperlink source scope", wb.LocalNamedRanges)
	}
	link, ok := wb.resolveHyperlinkLocation("局部", "原表", 0)
	if !ok || link.Sheet != "原表" || link.Ref != "D9" {
		t.Fatal("hyperlink used a different scope from the catalogue", link, ok)
	}
	result, err := wb.ReadCatalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	item := catalogueFind(t, result, "局部", "原表")
	if item.Status != "ready" || item.Areas[0].Start != link.Ref {
		t.Fatal(item, link)
	}
}
