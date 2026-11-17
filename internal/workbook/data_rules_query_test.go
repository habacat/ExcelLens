package workbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func TestDataRuleQueriesPageArraysAndKeepDefinitionsImmutable(t *testing.T) {
	var refs, values []string
	for at := 1; at <= 121; at++ {
		refs = append(refs, fmt.Sprintf("A%d", at))
		values = append(values, fmt.Sprintf("%03d", at))
	}
	definition := `<dataValidation type="list" sqref="` + strings.Join(refs, " ") + `"><formula1>"` + strings.Join(values, ",") + `"</formula1></dataValidation>`
	index, err := scanDataRuleFixture(`<worksheet><sheetData/><dataValidations count="1">` + definition + `</dataValidations></worksheet>`)
	if err != nil {
		t.Fatal(err)
	}
	page, err := index.List(context.Background(), DataRuleQuery{Count: formats.DataRulePageRows})
	if err != nil || len(page.Items) != 1 || !page.Items[0].Shortened || page.Items[0].RangeCount != 121 || page.Items[0].ListItems != 121 || !page.ScopeComplete || index.BudgetBytes() <= 0 {
		t.Fatal(page, err)
	}
	encoded, _ := json.Marshal(page)
	for _, forbidden := range []string{`"rawXml"`, `"inlineList"`, `"formula1"`, `"attributes"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatal("list serialized full definitions", forbidden)
		}
	}
	detail, err := index.Detail(1, 50, 100, formats.DataRulePageRows)
	if err != nil || detail.RangeCount != 121 || detail.ListCount != 121 || len(detail.Item.Ranges) != 50 || len(detail.Item.InlineList) != 21 || detail.Item.Ranges[0].Row != 51 || detail.Item.InlineList[0] != "101" || detail.Item.RawXML != definition || detail.Item.SqRef != strings.Join(refs, " ") {
		t.Fatal(detail, err)
	}
	detail.Item.Ranges[0].Row = 999
	detail.Item.InlineList[0] = "changed"
	detail.Item.Attributes["type"] = "custom"
	again, err := index.Detail(1, 50, 100, formats.DataRulePageRows)
	if err != nil || again.Item.Ranges[0].Row != 51 || again.Item.InlineList[0] != "101" || again.Item.Attributes["type"] != "list" {
		t.Fatal("detail mutated shared metadata", again, err)
	}
	area, err := index.Area(1, 120)
	if err != nil || area.Row != 121 || area.Col != 1 {
		t.Fatal(area, err)
	}
	for _, args := range [][4]int{{0, 0, 0, 50}, {2, 0, 0, 50}, {1, -1, 0, 50}, {1, 122, 0, 50}, {1, 0, 122, 50}, {1, 0, 0, 51}} {
		if _, err := index.Detail(args[0], args[1], args[2], args[3]); err == nil {
			t.Fatal("invalid detail bounds accepted", args)
		}
	}
	if _, err := index.Area(1, 121); err == nil {
		t.Fatal("out-of-range source rectangle accepted")
	}
}

func TestDataRuleListMatchesAtWithoutDuplicatesOrInventedCoverage(t *testing.T) {
	index, err := scanDataRuleFixture(`<worksheet><sheetData/><dataValidations count="3"><dataValidation type="whole" sqref="A1:A1048576 A2:A4"><formula1>1</formula1><formula2>9</formula2></dataValidation><dataValidation type="list" sqref="A1"><formula1>NamedList</formula1></dataValidation><dataValidation type="custom" sqref="XFE1"><formula1>A1&gt;0</formula1></dataValidation></dataValidations></worksheet>`)
	if err != nil {
		t.Fatal(err)
	}
	at, err := index.At(context.Background(), 1, 1, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	page, err := index.List(context.Background(), DataRuleQuery{Row: 1, Col: 1, Count: 50})
	if err != nil || len(page.Items) != 2 || page.ScopeComplete || page.Matched != at.Total || !reflect.DeepEqual([]int{page.Items[0].ID, page.Items[1].ID}, []int{at.Items[0].ID, at.Items[1].ID}) {
		t.Fatal(page, at, err)
	}
	issues, err := index.List(context.Background(), DataRuleQuery{Status: "issues", Count: 50})
	if err != nil || len(issues.Items) != 2 || issues.Items[0].Status != "unresolved" || issues.Items[1].Status != "invalid" {
		t.Fatal(issues, err)
	}
	missing, err := index.List(context.Background(), DataRuleQuery{Row: 10, Col: 2, Count: 50})
	if err != nil || len(missing.Items) != 0 || missing.ScopeComplete {
		t.Fatal("empty hits claimed absence despite unknown source scope", missing, err)
	}
	for _, query := range []DataRuleQuery{{Count: 0}, {Count: 51}, {Row: 1, Count: 50}, {Col: -1, Count: 50}, {Row: formats.ExcelRows + 1, Col: 1, Count: 50}, {Status: "valid", Count: 50}, {Type: "unknown", Count: 50}, {Offset: 4, Count: 50}} {
		if _, err := index.List(context.Background(), query); err == nil {
			t.Fatal("invalid list query accepted", query)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := index.List(ctx, DataRuleQuery{Count: 50}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled list published a page", err)
	}
}
