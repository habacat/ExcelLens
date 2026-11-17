package workbook

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"excellens/internal/formats"
)

func assertCatalogueResolution(t *testing.T, input, result Catalogue) {
	t.Helper()
	if len(result.Items) > formats.CatalogueEntries {
		t.Fatal("entry capacity exceeded")
	}
	definitions := make(map[string]string, len(input.Items))
	for _, item := range input.Items {
		definitions[item.ID] = item.Definition
	}
	for _, item := range result.Items {
		if definitions[item.ID] != item.Definition || len(item.Areas) > formats.CatalogueAreas {
			t.Fatal("definition changed or area capacity exceeded", item)
		}
		if item.Status != "ready" && len(item.Areas) > 0 {
			t.Fatal("unresolved entry exposes a misleading target", item)
		}
		for _, area := range item.Areas {
			if area.Row < 1 || area.EndRow < area.Row || area.EndRow > formats.ExcelRows || area.Col < 1 || area.EndCol < area.Col || area.EndCol > formats.ExcelColumns {
				t.Fatal("invalid source coordinates", item)
			}
		}
	}
}

func FuzzWorksheetTableDeclarations(f *testing.F) {
	f.Add(`<x:tableParts count="1"><x:tablePart r:id="r1"/></x:tableParts>`)
	f.Add(`<x:tableParts count="2"><x:tablePart r:id="r1"/><x:tablePart r:id="r1"/></x:tableParts>`)
	f.Add(`<x:tableParts><x:tablePart id="missing-namespace"/></x:tableParts>`)
	f.Fuzz(func(t *testing.T, block string) {
		if len(block) > 4096 {
			return
		}
		seed := &worksheetCatalogueSeed{root: `<x:worksheet xmlns:x="` + catalogueTestNS + `" xmlns:r="` + catalogueTestRel + `">`, rootQName: "x:worksheet"}
		ids, err := readWorksheetTableBlock(context.Background(), seed, []byte(block), catalogueTestNS)
		if err != nil {
			return
		}
		if !validWorksheetDeclarations(worksheetCatalogueDeclarations{IDs: ids}) {
			t.Fatal("declaration parser returned an invalid proof", ids)
		}
		second, err := readWorksheetTableBlock(context.Background(), seed, []byte(block), catalogueTestNS)
		if err != nil || !reflect.DeepEqual(ids, second) {
			t.Fatal("declaration parsing changed on repeat", ids, second, err)
		}
	})
}

func FuzzCatalogueBIFF8NameRecords(f *testing.F) {
	f.Add(biffCatalogueNameFixture("甲", 0, 0, biffCatalogueRefFixture(0, 1, 2)))
	f.Add(biffCatalogueNameFixture("循环", 0, 1, biffCatalogueAliasFixture(1)))
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8224 {
			return
		}
		capture := new(biffCatalogueCapture)
		capture.capture(0x0085, biffCatalogueSheetFixture("原表", 0))
		capture.capture(0x01AE, []byte{1, 0, 1, 4})
		capture.capture(0x0017, []byte{1, 0, 0, 0, 0, 0, 0, 0})
		capture.capture(0x0018, data)
		input := capture.finish()
		before, _ := json.Marshal(input)
		result := catalogueFixtureBook().ResolveCatalogue(input)
		assertCatalogueResolution(t, input, result)
		after, _ := json.Marshal(input)
		if string(before) != string(after) || !reflect.DeepEqual(result, catalogueFixtureBook().ResolveCatalogue(input)) {
			t.Fatal("resolution mutated source metadata or changed on repeat")
		}
	})
}

func FuzzCatalogueNamesXML(f *testing.F) {
	f.Add(`<workbook><definedNames><definedName name="甲">'原表'!$A$1:$B$2</definedName></definedNames></workbook>`)
	f.Add(`<workbook><definedNames><definedName name="甲">乙</definedName><definedName name="乙">甲</definedName></definedNames></workbook>`)
	f.Add(`<workbook><definedNames><definedName name="甲" localSheetId="1">$A$1</definedName></definedNames></workbook>`)
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 4096 {
			return
		}
		input, err := catalogueNamesXML([]byte(source), []workbookSheetXML{{Name: "图表"}, {Name: "原表"}})
		if err != nil {
			return
		}
		before, _ := json.Marshal(input)
		result := catalogueFixtureBook().ResolveCatalogue(input)
		assertCatalogueResolution(t, input, result)
		after, _ := json.Marshal(input)
		if string(before) != string(after) {
			t.Fatal("resolution changed source definitions")
		}
	})
}
