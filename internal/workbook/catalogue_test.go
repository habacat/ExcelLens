package workbook

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func catalogueFixtureBook() *Workbook {
	return &Workbook{Format: "xlsx", Sheets: []SheetMeta{{Name: "原表", Path: "xl/worksheets/sheet1.xml"}, {Name: "O'Brien, 明细", Path: "xl/worksheets/sheet2.xml"}, {Name: "隐藏表", Path: "xl/worksheets/sheet3.xml", State: "hidden"}}}
}

func catalogueName(name, scope, definition string) CatalogueItem {
	return CatalogueItem{Kind: "name", Name: name, Scope: scope, Definition: definition, Status: "unresolved"}
}

func catalogueFind(t *testing.T, catalogue Catalogue, name, scope string) CatalogueItem {
	t.Helper()
	for _, item := range catalogue.Items {
		if item.Name == name && item.Scope == scope {
			return item
		}
	}
	t.Fatal(name, scope, catalogue)
	return CatalogueItem{}
}

func TestCatalogueNamesRespectRootNamespaceSourceOrderScopeHiddenAndDuplicateAmbiguity(t *testing.T) {
	original := []workbookSheetXML{{Name: "图表页"}, {Name: "原表"}, {Name: "隐藏表", State: "hidden"}}
	data := []byte(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:e="urn:extension"><definedNames><definedName name="编号" comment="保留原文"> '原表'!$B$2:$B$9 </definedName><definedName name="编号" localSheetId="1" hidden="1">'原表'!$D$2</definedName><definedName name="坏作用域" localSheetId="9">'原表'!$A$1</definedName><definedName name="重复">'原表'!$A$2</definedName><definedName name="重复">'原表'!$B$3</definedName><definedName name="宏" xlm="true">'原表'!$E$1</definedName><definedName name="嵌套">'原表'!$A$1<e:x>bad</e:x></definedName><e:definedName name="伪名称">'原表'!$Z$1</e:definedName></definedNames><extLst><ext><definedName name="扩展伪名称">'原表'!$Z$2</definedName></ext></extLst></workbook>`)
	catalogue, err := catalogueNamesXML(data, original)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalogue.Items) != 7 {
		t.Fatal("extensions leaked into directory", catalogue)
	}
	before, _ := json.Marshal(catalogue)
	resolved := catalogueFixtureBook().ResolveCatalogue(catalogue)
	global := catalogueFind(t, resolved, "编号", "")
	if global.Definition != " '原表'!$B$2:$B$9 " || global.Comment != "保留原文" || global.Status != "ready" || global.Areas[0].Start != "B2" || global.Areas[0].End != "B9" {
		t.Fatal(global)
	}
	local := catalogueFind(t, resolved, "编号", "原表")
	if !local.Hidden || local.ScopeID != "1" || local.Areas[0].Start != "D2" {
		t.Fatal("scope was renumbered after dropping chart sheet", local)
	}
	for _, name := range []string{"坏作用域", "重复", "宏", "嵌套"} {
		item := catalogueFind(t, resolved, name, "")
		if item.Status == "ready" || item.Reason == "" || len(item.Areas) != 0 {
			t.Fatal(item)
		}
	}
	after, _ := json.Marshal(catalogue)
	if string(before) != string(after) {
		t.Fatal("resolution mutated source definitions")
	}
	globalMap, localMap := readNamedRanges(data, original)
	if globalMap["重复"] != "" || globalMap["伪名称"] != "" || globalMap["扩展伪名称"] != "" || !strings.Contains(localMap["原表"]["编号"], "D$2") {
		t.Fatal(globalMap, localMap)
	}
}

func TestCatalogueAbsoluteAreasAndAliasGraphsNeverGuessRelativeExternalOrCyclicTargets(t *testing.T) {
	book := catalogueFixtureBook()
	items := []CatalogueItem{
		catalogueName("全局", "", "'原表'!$B$2:$C$4"), catalogueName("全局", "隐藏表", "$D$9"), catalogueName("本地别名", "隐藏表", "全局"),
		catalogueName("联合", "", "'O''Brien, 明细'!$A$1,'原表'!$F$2:$G$3"), catalogueName("整列", "", "'原表'!$A:$C"), catalogueName("整行", "隐藏表", "$2:$5"),
		catalogueName("循环甲", "", "循环乙"), catalogueName("循环乙", "", "循环甲"), catalogueName("相对", "", "'原表'!A1"), catalogueName("半相对", "", "'原表'!$A1"),
		catalogueName("外部", "", "'[other.xlsx]原表'!$A$1"), catalogueName("三维", "", "'原表:隐藏表'!$A$1"), catalogueName("未限定", "", "$A$1"), catalogueName("常量", "", "12.50"),
		catalogueName("坏坐标", "", "'原表'!$XFE$1"), catalogueName("反向", "", "'原表'!$C$4:$B$2"), catalogueName("越界行", "", "'原表'!$A$1048577"), catalogueName("函数", "", "OFFSET('原表'!$A$1,0,0)"),
	}
	resolved := book.ResolveCatalogue(Catalogue{Items: items, Complete: true})
	if got := catalogueFind(t, resolved, "本地别名", "隐藏表"); got.Status != "ready" || got.Areas[0].Sheet != "隐藏表" || got.Areas[0].Start != "D9" {
		t.Fatal("local name did not shadow global", got)
	}
	if got := catalogueFind(t, resolved, "联合", ""); got.Status != "ready" || len(got.Areas) != 2 || got.Areas[0].Sheet != "O'Brien, 明细" || got.Areas[1].Start != "F2" {
		t.Fatal(got)
	}
	if got := catalogueFind(t, resolved, "整列", ""); got.Areas[0].End != "C1048576" {
		t.Fatal(got)
	}
	if got := catalogueFind(t, resolved, "整行", "隐藏表"); got.Areas[0].End != "XFD5" {
		t.Fatal(got)
	}
	for _, item := range resolved.Items[6:] {
		if item.Status == "ready" || len(item.Areas) != 0 || item.Definition != items[indexCatalogueName(items, item.Name)].Definition {
			t.Fatal(item)
		}
	}
	book.Format = "xls"
	old := book.ResolveCatalogue(Catalogue{Items: []CatalogueItem{catalogueName("整列", "", "'原表'!$A:$IV"), catalogueName("越界", "", "'原表'!$A$65537")}, Complete: true})
	if old.Items[0].Areas[0].End != "IV65536" || old.Items[1].Status == "ready" {
		t.Fatal(old)
	}
}

func indexCatalogueName(items []CatalogueItem, name string) int {
	for i, item := range items {
		if item.Name == name {
			return i
		}
	}
	return -1
}

func TestCatalogueTableColumnsAndExplicitSlicesUseOnlyConfirmedSourceDefinition(t *testing.T) {
	data := []byte(`<table xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" id="1" name="账表" displayName="账表" ref="B3:D9" headerRowCount="1" totalsRowCount="1"><tableColumns count="3"><tableColumn id="20" name="编号"/><tableColumn id="5" name="金额"/><tableColumn id="99" name="备注"/></tableColumns></table>`)
	item, err := catalogueTableXML(data, "原表", "xl/tables/table1.xml", "rTable")
	if err != nil {
		t.Fatal(err)
	}
	book := catalogueFixtureBook()
	names := []CatalogueItem{item, catalogueName("全部", "", "账表[#All]"), catalogueName("数据", "", "账表[#Data]"), catalogueName("表头", "", "账表[#Headers]"), catalogueName("合计", "", "账表[#Totals]"), catalogueName("金额列", "", "账表[金额]"), catalogueName("裸表名", "", "账表"), catalogueName("错误工作表", "", "'隐藏表'!账表[金额]")}
	before := book.ResolveCatalogue(Catalogue{Items: names, Complete: true})
	for _, entry := range before.Items {
		if entry.Status == "ready" {
			t.Fatal("unconfirmed table became a valid target", entry)
		}
	}
	item.Table.Verified = true
	after := book.ResolveCatalogue(Catalogue{Items: names, Complete: true})
	for name, refs := range map[string][2]string{"全部": {"B3", "D9"}, "数据": {"B4", "D8"}, "表头": {"B3", "D3"}, "合计": {"B9", "D9"}, "金额列": {"C4", "C8"}, "裸表名": {"B4", "D8"}} {
		got := catalogueFind(t, after, name, "")
		if got.Status != "ready" || got.Areas[0].Start != refs[0] || got.Areas[0].End != refs[1] {
			t.Fatal(name, got)
		}
	}
	if wrong := catalogueFind(t, after, "错误工作表", ""); wrong.Status == "ready" || len(wrong.Areas) > 0 {
		t.Fatal("qualified table reference ignored the source sheet", wrong)
	}
	for _, bad := range []string{strings.Replace(string(data), `count="3"`, `count="2"`, 1), strings.Replace(string(data), `id="5"`, `id="20"`, 1), strings.Replace(string(data), `ref="B3:D9"`, `ref="B3:E9"`, 1), strings.Replace(string(data), `headerRowCount="1"`, `headerRowCount="2"`, 1), string(data) + `<table/>`} {
		if _, err := catalogueTableXML([]byte(bad), "原表", "p", "r"); err == nil {
			t.Fatal("invalid table accepted", bad)
		}
	}
}

func TestCatalogueMalformedXMLDuplicateAttributesAndDepthCannotManufactureSourceTargets(t *testing.T) {
	for _, data := range []string{
		`<workbook><definedNames><definedName name="甲" name="乙">'原表'!$A$1</definedName></definedNames></workbook>`,
		`<workbook><definedNames><definedName name="甲">'原表'!$A$1</definedName></definedNames></workbook>tail`,
		`<workbook>` + strings.Repeat(`<ext>`, formats.CatalogueXMLDepth) + strings.Repeat(`</ext>`, formats.CatalogueXMLDepth) + `</workbook>`,
	} {
		if _, err := catalogueNamesXML([]byte(data), nil); err == nil {
			t.Fatal("invalid source name XML accepted", data[:min(120, len(data))])
		}
	}
	for _, data := range []string{
		`<Relationships><Relationship Id="a" Id="b" Type="x" Target="y"/></Relationships>`,
		`<Relationships><Relationship Id="a" Type="x" Target="y"/><Relationship Id="a" Type="x" Target="z"/></Relationships>`,
		`<Relationships/>tail`,
		`<Relationships>` + strings.Repeat(`<ext>`, formats.CatalogueXMLDepth) + strings.Repeat(`</ext>`, formats.CatalogueXMLDepth) + `</Relationships>`,
	} {
		if _, err := catalogueRelationshipsXML([]byte(data)); err == nil {
			t.Fatal("invalid source relationship XML accepted", data[:min(120, len(data))])
		}
	}
	base := `<table name="账表" ref="A1:B3"><tableColumns count="2"><tableColumn id="1" name="甲"/><tableColumn id="2" name="乙"/></tableColumns></table>`
	for _, data := range []string{
		strings.Replace(base, `id="2"`, `id="01"`, 1),
		strings.Replace(base, `id="2"`, `id="0"`, 1),
		strings.Replace(base, `id="2"`, `id="4294967296"`, 1),
		strings.Replace(base, `ref="A1:B3"`, `ref="A1:B3" ref="C1:D3"`, 1),
		strings.Replace(base, `</table>`, `<tableColumns count="2"/></table>`, 1),
		base + "tail",
	} {
		if _, err := catalogueTableXML([]byte(data), "原表", "p", "r"); err == nil {
			t.Fatal("invalid source table XML accepted", data)
		}
	}
}

func TestCatalogueMissingNamesCannotHideDuplicateOrScopeShadowingAndHyperlinksFailClosed(t *testing.T) {
	var source strings.Builder
	source.WriteString(`<workbook><definedNames><definedName name="首项">'原表'!$A$1</definedName>`)
	source.WriteString(`<definedName name="超限">` + strings.Repeat("x", formats.CatalogueDefinitionMax+1) + `</definedName>`)
	source.WriteString(`</definedNames></workbook>`)
	parsed, err := catalogueNamesXML([]byte(source.String()), nil)
	if err != nil || parsed.Complete || !parsed.NamesIncomplete || len(parsed.Items) != 1 {
		t.Fatal(parsed, err)
	}
	resolved := catalogueFixtureBook().ResolveCatalogue(parsed)
	if item := resolved.Items[0]; item.Status == "ready" || item.Reason == "" || len(item.Areas) != 0 || item.Definition != "'原表'!$A$1" {
		t.Fatal("partial name list assumed no unseen duplicate or local shadow", item)
	}
	global, local := readNamedRanges([]byte(source.String()), nil)
	if len(global) != 0 || len(local) != 0 {
		t.Fatal("hyperlinks used partially read names", global, local)
	}
}

func TestCatalogueRelationshipPathsAndTrueSourceOrderExcludeNestedOrExtensionLookalikes(t *testing.T) {
	source := `<workbook xmlns="` + catalogueTestNS + `" xmlns:r="` + catalogueTestRel + `" xmlns:e="urn:extension"><sheets><sheet name="图表" r:id="c"/><e:sheet name="伪名称" r:id="fake"/><sheet name="原表" r:id="s"/></sheets><extLst><sheet name="嵌套伪名称" r:id="nested"/></extLst></workbook>`
	sheets, err := catalogueSheetsXML([]byte(source))
	if err != nil || len(sheets) != 2 || sheets[0].Name != "图表" || sheets[1].Name != "原表" {
		t.Fatal("extension changed source scope ordinals", sheets, err)
	}
	for _, target := range []string{"../../../outside.xml", "https://example.invalid/x", "//host/table.xml", "../tables/t.xml?q=1", "../tables/t.xml#fragment", "../tables/%00.xml", "../tables/t.xml?"} {
		if part, err := catalogueRelationTarget("xl/worksheets/sheet1.xml", target); err == nil {
			t.Fatal("ambiguous relationship path accepted", target, part)
		}
	}
	for _, target := range []string{"../tables/table%201.xml", "/xl/tables/table%201.xml"} {
		if part, err := catalogueRelationTarget("xl/worksheets/sheet1.xml", target); err != nil || part != "xl/tables/table 1.xml" {
			t.Fatal(target, part, err)
		}
	}
}

func TestCatalogueBudgetDepthAndLargeUnionsRemainExplicitWithoutPartialRanges(t *testing.T) {
	b := newCatalogueBuilder()
	oversize := catalogueName("过长", "", strings.Repeat("x", formats.CatalogueDefinitionMax+1))
	if b.add(oversize) || b.value.Complete || len(b.value.Notices) == 0 {
		t.Fatal(b)
	}
	items := []CatalogueItem{catalogueName("终点", "", "'原表'!$A$1")}
	for i := 0; i < formats.CatalogueDepth+2; i++ {
		name := fmt.Sprintf("别名%d", i)
		value := "终点"
		if i > 0 {
			value = fmt.Sprintf("别名%d", i-1)
		}
		items = append(items, catalogueName(name, "", value))
	}
	items = append(items, catalogueName("太多区域", "", strings.Repeat("'原表'!$A$1,", formats.CatalogueAreas)+"'原表'!$A$2"))
	result := catalogueFixtureBook().ResolveCatalogue(Catalogue{Items: items, Complete: true})
	if result.Items[len(result.Items)-2].Status == "ready" || result.Items[len(result.Items)-1].Status == "ready" {
		t.Fatal("depth or area budget ignored", result.Items[len(result.Items)-2:])
	}
	if !reflect.DeepEqual(items[0].Areas, []CatalogueArea(nil)) {
		t.Fatal("source model modified")
	}
}

func TestCatalogueResolveWorkBudgetPreservesInvalidStatesAndUnicodeSheetIdentity(t *testing.T) {
	book := catalogueFixtureBook()
	book.Sheets = append(book.Sheets, SheetMeta{Name: "Σ", Path: "xl/worksheets/sigma.xml"})
	items := []CatalogueItem{
		catalogueName("Unicode表", "", "'ς'!$B$3"),
		catalogueName("终点", "", "'原表'!$A$1"),
		catalogueName("联合", "", strings.Repeat("终点,", formats.CatalogueAreas-1)+"终点"),
	}
	for i := 0; i < 4500; i++ {
		items = append(items, catalogueName(fmt.Sprintf("展开%d", i), "", "联合,联合"))
	}
	invalid := catalogueName("源损坏", "", "保留源定义")
	invalid.Status, invalid.Reason = "invalid", "源记录损坏"
	invalid.Areas = []CatalogueArea{{Sheet: "原表", Start: "A1", End: "A1", Row: 1, Col: 1, EndRow: 1, EndCol: 1}}
	items = append(items, invalid)
	result := book.ResolveCatalogue(Catalogue{Items: items, Complete: true})
	if first := result.Items[0]; first.Status != "ready" || first.Areas[0].Sheet != "Σ" || first.Areas[0].Start != "B3" {
		t.Fatal("Unicode sheet matching differs from EqualFold", first)
	}
	if result.Complete || len(result.Notices) == 0 || len(result.Items) != len(items) {
		t.Fatal("work exhaustion hidden or original entries lost", result.Complete, result.Notices, len(result.Items))
	}
	if last := result.Items[len(result.Items)-1]; last.Status != "invalid" || last.Reason != "源记录损坏" || last.Definition != "保留源定义" || len(last.Areas) != 0 {
		t.Fatal("work exhaustion overwrote invalid source status or exposed stale range", last)
	}
	if len(items[len(items)-1].Areas) != 1 {
		t.Fatal("resolution changed caller source state")
	}
}
