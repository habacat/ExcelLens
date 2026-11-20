package editfile

import (
	"context"
	"encoding/xml"
	"slices"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func TestPivotTypedCacheFlagsAndDateResults(t *testing.T) {
	records := pivotRecords{
		dicts: []pivotDictionary{{index: map[pivotValue]int{}}, {}, {}},
		rows: [][]pivotValue{
			{{"b", "1"}, {"n", "1250"}, {"d", "2025-10-20T00:00:00"}},
			{{"e", "#N/A"}, {"n", "-3.5"}, {"m", ""}},
		},
	}
	for _, row := range records.rows {
		records.dicts[0].add(row[0])
	}
	for i, want := range []map[string]string{
		{"count": "2", "containsString": "", "containsMixedTypes": ""},
		{"count": "", "containsNumber": "1", "minValue": "-3.5", "maxValue": "1250", "containsInteger": ""},
		{"count": "", "containsDate": "1", "containsNonDate": "0", "containsBlank": "1", "maxDate": "2025-10-21T00:00:00"},
	} {
		dec := xml.NewDecoder(strings.NewReader(records.sharedItems(i)))
		token, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range want {
			if got := attribute(token.(xml.StartElement), "", key); got != value {
				t.Errorf("field %d %s = %q, want %q", i, key, got, value)
			}
		}
	}
	b := &CalculationBook{date1904: true, pivotDateFormats: map[string]workbook.CalendarNumberFormat{"1": {ID: 22}}}
	date, err := b.pivotCell(editdoc.Value{Kind: "number", Raw: "45950.123456789", Style: "1"})
	if err != nil || date != (pivotValue{"d", "2029-10-21T02:57:47"}) {
		t.Fatalf("date item %+v, %v", date, err)
	}
	d := pivotDimension{date1904: true, dateStyle: "1", values: []pivotValue{date}, items: []pivotItemXML{{X: 0}}}
	v, err := d.label(0, pivotCaptions{})
	if err != nil || v.Kind != "number" || v.Style != "1" || validateStoredDouble(v.Raw) != nil || validateXLSXNumber(v.Raw) == nil {
		t.Fatalf("calculated date label %+v, %v", v, err)
	}
}

func TestPivotHierarchyAggregatesSourceRecordsAtEveryIntersection(t *testing.T) {
	raw := []byte(`<pivotTableDefinition xmlns="` + mainNS + `" name="Sales" dataCaption="Values"><location ref="D5:E7" firstHeaderRow="1" firstDataRow="1" firstDataCol="1"/><pivotFields count="6"><pivotField axis="axisRow" showAll="0"/><pivotField showAll="0"/><pivotField showAll="0"/><pivotField showAll="0"/><pivotField showAll="0"/><pivotField dataField="1"/></pivotFields><rowFields count="1"><field x="0"/></rowFields><rowItems count="1"><i><x/></i></rowItems><colItems count="1"><i/></colItems><dataFields count="1"><dataField name="Average" fld="5" subtotal="average"/></dataFields></pivotTableDefinition>`)
	var table pivotTableXML
	if err := xml.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	plan := pivotPlan{xml: raw, table: table, sheet: "Summary", old: cellRect{5, 4, 7, 5}, rows: []int{0, 1}, cols: []int{2, 3}, pages: []int{4}, data: 5, aggregation: "average", selections: map[int][]editdoc.PivotItem{4: {{Type: "s", Value: "Open"}}}, keepFilter: true}
	rec := pivotRecords{dicts: make([]pivotDictionary, 6)}
	for _, name := range []string{"Region", "Team", "Channel", "Period", "Status", "Amount"} {
		rec.fields = append(rec.fields, cacheFieldXML{Name: name})
	}
	for _, input := range [][]string{{"East", "A", "Web", "P1", "Open", "10"}, {"East", "A", "Web", "P1", "Open", "14"}, {"East", "B", "Shop", "P2", "Open", "30"}, {"West", "A", "Shop", "P2", "Closed", "100"}, {"East", "C", "Shop", "P2", "Open", ""}} {
		line := make([]pivotValue, len(input))
		for i, v := range input {
			line[i] = pivotValue{"s", v}
			if i == 5 {
				line[i] = pivotValue{"n", v}
				if v == "" {
					line[i] = pivotValue{"m", ""}
				}
				continue
			}
			if rec.dicts[i].index == nil {
				rec.dicts[i].index = map[pivotValue]int{}
			}
			rec.dicts[i].add(line[i])
		}
		rec.rows = append(rec.rows, line)
	}
	raw, cells, err := plan.render(t.Context(), rec, objectValues{book: &CalculationBook{}, calculation: &Calculation{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		r, c      int
		kind, raw string
	}{{3, 4, "text", "Status"}, {3, 5, "text", "Open"}, {6, 6, "text", "Shop Total"}, {8, 4, "text", "East"}, {8, 9, "number", "18"}, {9, 7, "number", "12"}, {10, 5, "number", "30"}, {11, 9, "blank", ""}, {12, 4, "text", "Grand Total"}, {12, 9, "number", "18"}} {
		v := cells[editdoc.Address{Sheet: "Summary", Row: test.r, Col: test.c}]
		if v.Kind != test.kind || v.Raw != test.raw {
			t.Errorf("row %d col %d: got %+v, want %s %s", test.r, test.c, v, test.kind, test.raw)
		}
	}
	var saved pivotTableXML
	if err := xml.Unmarshal(raw, &saved); err != nil || saved.Area.Ref != "D5:I12" || saved.Area.PageRows != 1 || len(saved.RowFields) != 2 || len(saved.ColFields) != 2 || len(saved.PageFields) != 1 || saved.PageFields[0].Item == nil {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestPivotPageOwnershipAndSchemaRebinding(t *testing.T) {
	var table pivotTableXML
	if err := xml.Unmarshal([]byte(`<pivotTableDefinition pageOverThenDown="1" pageWrap="2"><location ref="D7:J14" rowPageCount="2" colPageCount="2"/><pageFields><pageField fld="1"/><pageField fld="2"/><pageField fld="3"/></pageFields></pivotTableDefinition>`), &table); err != nil {
		t.Fatal(err)
	}
	got, err := table.oldPageAreas(cellRect{7, 4, 14, 10})
	want := []cellRect{{4, 4, 4, 5}, {4, 7, 4, 8}, {5, 4, 5, 5}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("pages %v %v", got, err)
	}
	values := objectValues{book: &CalculationBook{}, calculation: &Calculation{}, overlay: map[editdoc.Address]editdoc.Value{}}
	for i, name := range []string{"New", "Amount", "Amount", "Keep"} {
		values.overlay[editdoc.Address{Sheet: "Source", Row: 1, Col: i + 1}] = editdoc.Value{Kind: "text", Raw: name}
	}
	s, err := readPivotSchema(values, pivotSource{sheet: "Source", area: cellRect{1, 1, 4, 4}}, []string{"Removed", "Keep", "Amount"})
	if err != nil || !slices.Equal(s.names, []string{"New", "Amount", "Amount2", "Keep"}) || !slices.Equal(s.newForOld, []int{-1, 3, 1}) {
		t.Fatalf("%+v %v", s, err)
	}
	info := s.info(PivotInfo{Fields: []string{"Removed", "Keep", "Amount"}, Data: "Amount", Axes: &editdoc.PivotAxes{Rows: []string{"Removed", "Keep"}}})
	if !slices.Equal(info.Axes.Rows, []string{"Keep"}) || info.Data != "Amount" {
		t.Fatalf("%+v", info)
	}
	raw := []byte(`<pivotTableDefinition xmlns="` + mainNS + `"><pivotFields count="3"><pivotField/><pivotField name="alias"/><pivotField/></pivotFields><rowFields><field x="0"/><field x="1"/></rowFields><dataFields><dataField fld="2"/></dataFields></pivotTableDefinition>`)
	out, err := s.table(context.Background(), raw)
	if err != nil || !strings.Contains(string(out), `<field x="3"/>`) || !strings.Contains(string(out), `<dataField fld="1"/>`) || strings.Contains(string(out), `<field x="0"/>`) {
		t.Fatalf("%s %v", out, err)
	}
}

func TestPivotMeasureReorderKeepsFilterAndOriginalXML(t *testing.T) {
	raw := []byte(`<pivotTableDefinition xmlns="` + mainNS + `"><dataFields count="3" xmlns:e="urn:test">
		<dataField name="Total" fld="1"/><dataField name="Earlier" fld="1" subtotal="average"/>
		<dataField name="Later" fld="1" subtotal="average" numFmtId="176" baseField="0"><extLst><e:format/></extLst></dataField>
	</dataFields></pivotTableDefinition>`)
	var table pivotTableXML
	if err := xml.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	plan := pivotPlan{table: table, rows: []int{0}, valueFilters: []pivotFilter{{field: 0, spec: editdoc.PivotValueFilter{Kind: "topCount", Measure: 2, Value: 1}}}}
	fields := []cacheFieldXML{{Name: "Category"}, {Name: "Amount"}}
	later, total := 2, 0
	change := &editdoc.Structure{Kind: "configurePivot", Measures: []editdoc.PivotMeasure{
		{Field: "Amount", Caption: "Peak & high", Aggregate: "max", SourceIndex: &later},
		{Field: "Amount", Caption: "Total", Aggregate: "sum", SourceIndex: &total},
	}}
	if err := plan.configureMeasures(fields, change, &editdoc.PivotAxes{Rows: []string{"Category"}}); err != nil {
		t.Fatal(err)
	}
	if err := plan.configureValueFilter(fields, change); err != nil {
		t.Fatal(err)
	}
	if len(plan.valueFilters) != 1 || plan.valueFilters[0].spec.Measure != 0 {
		t.Fatalf("filter after reordering: %+v", plan.valueFilters)
	}
	out, err := plan.writeMeasures(t.Context(), raw, "")
	if err != nil {
		t.Fatal(err)
	}
	var saved pivotTableXML
	if err := xml.Unmarshal(out, &saved); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved.DataFields, plan.measures) || saved.DataFields[0].Number != "176" || !strings.Contains(string(out), `baseField="0"`) || !strings.Contains(string(out), `xmlns:e="urn:test"`) || !strings.Contains(string(out), "<e:format/>") {
		t.Fatalf("measure lost original properties: %s", out)
	}
	plan.valueFilters = []pivotFilter{{field: 0, spec: editdoc.PivotValueFilter{Measure: 1}}}
	if err := plan.configureValueFilter(fields, change); err != nil || len(plan.valueFilters) != 0 {
		t.Fatalf("removed measure retained filter: %+v, %v", plan.valueFilters, err)
	}
}

func TestDateGroupingKeepsValuesOnTheirSideOfGroupedFields(t *testing.T) {
	s := pivotSchema{names: []string{"Date", "Team", "Month", "Year"}, newForOld: []int{0, 1}}
	group := dateReplacement{base: 0, old: map[int]bool{0: true}, names: []string{"Year", "Month"}}
	for pos := range 3 {
		axis := slices.Insert([]pivotAxisField{{X: 0}, {X: 1}}, pos, pivotAxisField{X: pivotValuesField})
		got, applied, position := group.axis(axis, s, true)
		if !applied || !slices.Equal(got, []string{"Year", "Month", "Team"}) || position != []int{0, 2, 3}[pos] {
			t.Fatalf("position %d became %d in %v", pos, position, got)
		}
	}
}

func TestUngroupSingleLevelStoredOnPhysicalDateField(t *testing.T) {
	base := 0
	fields := []cacheFieldXML{{Name: "Date", Group: &cacheDateGroup{Base: &base, Range: &cacheDateRange{Unit: "months"}}}}
	header := editdoc.Address{Sheet: "Source", Row: 1, Col: 1}
	values := objectValues{book: &CalculationBook{}, calculation: &Calculation{}, overlay: map[editdoc.Address]editdoc.Value{header: {Kind: "text", Raw: "Date"}}}
	source := pivotSource{sheet: "Source", area: cellRect{1, 1, 3, 1}}
	request := &editdoc.PivotDateGroup{Field: "Date"}
	_, dates, err := planDateSchema(values, source, fields, request, true)
	if err != nil || dates.replacement == nil || len(dates.levels) != 0 {
		t.Fatalf("ungroup existing field: %+v, %v", dates, err)
	}
	fields[0].Group = nil
	if _, _, err = planDateSchema(values, source, fields, request, true); err == nil {
		t.Fatal("ungroup accepted a raw date")
	}
}

func TestPivotNumberFormatMovesAcrossBlankIntersections(t *testing.T) {
	const styles = `<styleSheet xmlns="` + mainNS + `"><cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="176"/></cellXfs></styleSheet>`
	p := xlsxPackage{styles: 2}
	formats, err := measureFormats([]pivotDataField{{}, {Number: "176"}}, []pivotDataField{{Number: "176"}, {}})
	if err != nil {
		t.Fatal(err)
	}
	left := editdoc.Address{Sheet: "Report", Row: 2, Col: 1}
	right := editdoc.Address{Sheet: "Report", Row: 2, Col: 2}
	formats.output[left] = formats.byMeasure[0]
	plan := pivotPlan{sheet: left.Sheet, old: cellRect{1, 1, 2, 2}, formats: formats}
	plan.table.Area.Header = 1
	targets := &sheetTargets{rows: []rowTarget{{row: 2, cells: []cellTarget{
		{address: left, original: sheetCell{value: editdoc.Value{Kind: "blank"}}},
		{address: right, original: sheetCell{value: editdoc.Value{Kind: "number", Raw: "12", Style: "1"}}},
	}}}}
	edits := map[editdoc.Address]editdoc.Value{left: {Kind: "blank"}, right: {Kind: "number", Raw: "12"}}
	updates := map[string][]byte{"xl/styles.xml": []byte(styles)}
	if err := p.applyPivotFormats(t.Context(), left.Sheet, []pivotPlan{plan}, targets, edits, updates); err != nil {
		t.Fatal(err)
	}
	if edits[left].Style != "1" || edits[right].Style != "0" || edits[left].Kind != "blank" || string(updates["xl/styles.xml"]) != styles {
		t.Fatalf("formatted blank or old column: %+v", edits)
	}
}
