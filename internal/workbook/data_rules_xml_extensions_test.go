package workbook

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func dataRuleX14Fixture(body string) string {
	return `<extLst xmlns:k="` + x14DataRulesNamespace + `"><ext uri="` + x14DataRulesExtensionURI + `" xmlns:m="` + xmDataRulesNamespace + `" xmlns:l="` + x12DataRuleListNamespace + `">` + body + `</ext></extLst>`
}

func TestDataRulesX14RealZIPNamespacesRawDefinitionsAndContainerSettings(t *testing.T) {
	ordinary := `<dataValidations count="1" disablePrompts="0"><dataValidation type="list" sqref="A2:A9" showDropDown="1"><formula1>"001,普通"</formula1></dataValidation></dataValidations>`
	first := `<k:dataValidation type="list" allowBlank="true" showDropDown="true"><k:formula1><m:f>"001,中文,""引号"""</m:f></k:formula1><m:sqref>$B$2:$B$1048576 D2:D9</m:sqref></k:dataValidation>`
	second := `<k:dataValidation type="decimal" operator="between" showErrorMessage="1" errorStyle="stop"><k:formula1><m:f>9007199254740993.30</m:f></k:formula1><k:formula2><m:f>9007199254741015.11</m:f></k:formula2><m:sqref>A2:A1048576</m:sqref></k:dataValidation>`
	third := `<k:dataValidation type="list"><l:list>"含,逗号","001"</l:list><m:sqref>C2:C1048576</m:sqref></k:dataValidation>`
	w, meta, idx, file := dataRuleFixture(t, ordinary+dataRuleX14Fixture(`<k:dataValidations count="3" disablePrompts="true">`+first+second+third+`</k:dataValidations>`))
	before, _ := os.ReadFile(file)
	index, err := w.SourceDataRules(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := allDataRules(t, index)
	if len(items) != 4 || !index.Summary().Complete || index.Summary().Unresolved != 1 || index.Summary().PromptSettingsKnown || len(index.Summary().Containers) != 2 {
		t.Fatal(index.Summary())
	}
	if !items[0].SuppressDropDown || items[1].SuppressDropDown || !items[1].AllowBlank || items[1].Namespace != x14DataRulesNamespace || items[1].RawXML != first || !reflect.DeepEqual(items[1].InlineList, []string{"001", "中文", `"引号"`}) {
		t.Fatal(items[0], items[1])
	}
	if items[2].Formula1 != "9007199254740993.30" || items[2].Formula2 != "9007199254741015.11" || !items[2].ShowErrorMessage || items[2].RawXML != second {
		t.Fatal(items[2])
	}
	if items[3].ListDefinition != `"含,逗号","001"` || items[3].Status != "unresolved" || len(items[3].InlineList) != 0 || items[3].RawXML != third {
		t.Fatal("extended list was silently split or discarded", items[3])
	}
	page, err := index.At(context.Background(), 1048576, 2, 0, 50)
	if err != nil || page.Total != 1 || !page.ScopeComplete || page.Items[0].ID != 2 {
		t.Fatal(page, err)
	}
	summary := index.Summary()
	summary.Containers[1].Attributes["disablePrompts"] = "changed"
	summary.Containers[1].DisablePrompts = false
	again := index.Summary()
	if !again.Containers[1].DisablePrompts || again.Containers[1].Attributes["disablePrompts"] != "true" {
		t.Fatal("caller mutated shared source properties", again)
	}
	row, err := w.ReadRow(idx, 0, 1, 3)
	if err != nil || row.Cells[0].Raw != "001" || row.Cells[1].Raw != "9007199254740993.31" || !row.Cells[2].CacheMissing {
		t.Fatal(row, err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("x14 rule read changed source")
	}
}

func TestDataRulesX14RejectsFalseAncestorsNamespacesAndAmbiguousStructures(t *testing.T) {
	rule := `<k:dataValidation type="whole"><k:formula1><m:f>1</m:f></k:formula1><k:formula2><m:f>9</m:f></k:formula2><m:sqref>A2:A1048576</m:sqref></k:dataValidation>`
	extension := dataRuleX14Fixture(`<k:dataValidations count="1">` + rule + `</k:dataValidations>`)
	wrap := func(body string) string {
		return `<worksheet xmlns="` + catalogueTestNS + `"><sheetData/>` + body + `</worksheet>`
	}
	for name, body := range map[string]string{
		"wrong extension URI": strings.Replace(extension, x14DataRulesExtensionURI, "{UNKNOWN}", 1),
		"foreign ext":         strings.ReplaceAll(extension, `<ext uri=`, `<e:ext xmlns:e="urn:foreign" uri=`),
		"wrong x14 namespace": strings.Replace(extension, x14DataRulesNamespace, "urn:foreign", 1),
		"wrong parent":        strings.ReplaceAll(strings.ReplaceAll(extension, "<extLst ", "<other "), "</extLst>", "</other>"),
	} {
		if name == "foreign ext" {
			body = strings.Replace(body, "</ext>", "</e:ext>", 1)
		}
		t.Run(name, func(t *testing.T) {
			index, err := scanDataRuleFixture(wrap(body))
			if err != nil {
				t.Fatal(err)
			}
			if index.Summary().Rules != 0 {
				t.Fatal("non-source extension was accepted", index.Summary())
			}
			if name != "wrong parent" && index.Summary().Complete {
				t.Fatal("unread criteria became absence of rules", index.Summary())
			}
		})
	}
	for name, body := range map[string]string{
		"count mismatch":          strings.Replace(extension, `count="1"`, `count="2"`, 1),
		"duplicate x14 container": strings.Replace(extension, "</ext>", `<k:dataValidations count="0"/></ext>`, 1),
		"duplicate ext list":      extension + extension,
		"duplicate attribute":     strings.Replace(extension, `type="whole"`, `type="whole" type="decimal"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if index, err := scanDataRuleFixture(wrap(body)); err == nil || index != nil {
				t.Fatal("ambiguous source published partial criteria", index, err)
			}
		})
	}
	for name, changed := range map[string]string{
		"nested formula":        strings.Replace(rule, `<m:f>1</m:f>`, `<m:f>1<m:x>7</m:x>2</m:f>`, 1),
		"duplicate f":           strings.Replace(rule, `<m:f>1</m:f>`, `<m:f>1</m:f><m:f>2</m:f>`, 1),
		"missing sqref":         strings.Replace(rule, `<m:sqref>A2:A1048576</m:sqref>`, "", 1),
		"duplicate sqref":       strings.Replace(rule, `<m:sqref>A2:A1048576</m:sqref>`, `<m:sqref>A2:A9</m:sqref><m:sqref>B2:B9</m:sqref>`, 1),
		"wrong f namespace":     strings.Replace(rule, `<m:f>1</m:f>`, `<k:f>1</k:f>`, 1),
		"wrong sqref namespace": strings.ReplaceAll(rule, "m:sqref", "k:sqref"),
	} {
		t.Run(name, func(t *testing.T) {
			index, err := scanDataRuleFixture(wrap(dataRuleX14Fixture(`<k:dataValidations count="1">` + changed + `</k:dataValidations>`)))
			if err != nil {
				t.Fatal(err)
			}
			item := allDataRules(t, index)[0]
			if item.Status != "invalid" {
				t.Fatal("malformed rule was treated as valid metadata", item)
			}
			if strings.Contains(name, "formula") || strings.Contains(name, " f") {
				if item.Formula1 != "" {
					t.Fatal("unknown formula tree became a valid expression", item)
				}
			}
			if strings.Contains(name, "sqref") {
				page, err := index.At(context.Background(), 1, 1, 0, 50)
				if err != nil || page.ScopeComplete || len(item.Ranges) != 0 {
					t.Fatal(page, item, err)
				}
			}
		})
	}
}

func TestDataRulesX14SkipsUnrelatedLargeExtensionAndNeverReadsCellLookalikes(t *testing.T) {
	body := `<k:dataValidations count="1"><k:dataValidation type="none"><m:sqref>A1:A1048576</m:sqref></k:dataValidation></k:dataValidations>`
	extension := dataRuleX14Fixture(body)
	large := strings.Replace(extension, `<ext uri=`, `<ext uri="other"><payload>`+strings.Repeat("x", formats.DataRuleBlockBytes+100)+`</payload></ext><ext uri=`, 1)
	index, err := scanDataRuleFixture(`<worksheet><sheetData/>` + large + `</worksheet>`)
	if err != nil || !index.Summary().Complete || index.Summary().Rules != 1 || index.bytes > 64<<10 {
		t.Fatal("read retained or rejected the whole unrelated extLst", index, err)
	}
	fake := `<worksheet><sheetData><row r="1"><c r="A1">` + extension + `</c></row></sheetData></worksheet>`
	index, err = scanDataRuleFixture(fake)
	if err != nil || !index.Summary().Complete || index.Summary().Rules != 0 {
		t.Fatal("cell markup became worksheet criteria", index, err)
	}
}

func FuzzDataRuleX14(f *testing.F) {
	f.Add([]byte(`<worksheet><sheetData/>` + dataRuleX14Fixture(`<k:dataValidations count="1"><k:dataValidation type="list"><k:formula1><m:f>"001,中文"</m:f></k:formula1><m:sqref>A1:A1048576</m:sqref></k:dataValidation></k:dataValidations>`) + `</worksheet>`))
	f.Add([]byte(`<worksheet><sheetData/>` + dataRuleX14Fixture(`<k:dataValidations count="0"/>`) + `</worksheet>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128<<10 {
			t.Skip()
		}
		before := bytes.Clone(data)
		source, _ := formats.Lookup("xlsx")
		b := newDataRuleBuilder(source)
		if err := scanDataRuleXML(context.Background(), bytes.NewReader(data), int64(len(data)), b, nil); err != nil {
			return
		}
		index := b.finish()
		if !bytes.Equal(data, before) || len(index.rules) > formats.DataRuleCount || len(index.nodes) > formats.DataRuleRanges {
			t.Fatal("source/budget changed")
		}
		for _, rule := range index.rules {
			if rule.RawXML == "" || !bytes.Contains(data, []byte(rule.RawXML)) {
				t.Fatal("invented source rule")
			}
		}
	})
}

func TestDataRulesXMLUTF8BOMAndInheritedFormulaNamespace(t *testing.T) {
	text := "\uFEFF" + `<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="` + catalogueTestNS + `"><sheetData/>` + dataRuleX14Fixture(`<k:dataValidations count="1"><k:dataValidation type="whole"><k:formula1><f xmlns="`+xmDataRulesNamespace+`">1</f></k:formula1><k:formula2><m:f>9</m:f></k:formula2><m:sqref>A1:A1048576</m:sqref></k:dataValidation></k:dataValidations>`) + `</worksheet>`
	index, err := scanDataRuleFixture(text)
	if err != nil || !index.Summary().Complete || index.Summary().Rules != 1 {
		t.Fatal(index, err)
	}
	if allDataRules(t, index)[0].Formula1 != "1" {
		t.Fatal("formula namespace declaration was mistaken for unknown criteria")
	}
	if value, err := scanDataRuleFixture(" " + text); err == nil || value != nil {
		t.Fatal("BOM outside the start of the source was silently discarded", value, err)
	}
}
