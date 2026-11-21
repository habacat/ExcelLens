package server

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"testing"

	"excellens/internal/reconcile"
)

func TestKeyedVersionReportSeparatesMovesFromInsertedRows(t *testing.T) {
	isolateTestStorage(t)
	sheet := func(keys []string, formula bool) string {
		var xml strings.Builder
		xml.WriteString("<sheetData>")
		for i, key := range keys {
			n := i + 1
			fmt.Fprintf(&xml, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s</t></is></c><c r="B%d">`, n, n, key, n)
			if key == "001" && formula {
				xml.WriteString("<f>1+1</f>")
			}
			xml.WriteString("<v>2</v></c></row>")
		}
		xml.WriteString("</sheetData>")
		return xml.String()
	}
	left := editWorkbookFixture(t, map[string]string{"账": sheet([]string{"ID", "001", "B", "C", "D", "W", "dup", "dup", ""}, true)}, "", "")
	right := editWorkbookFixture(t, map[string]string{"账": sheet([]string{"ID", "X", "001", "C", "D", "B", "dup", "dup", ""}, false)}, "", "")
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.OpenPath(left); err != nil {
		t.Fatal(err)
	}
	id, err := a.OpenPathForLaunch(right)
	if err != nil {
		t.Fatal(err)
	}
	tab, err := a.sessionFor(id)
	if err != nil {
		t.Fatal(err)
	}
	l, r := a.currentWorkbook(), tab.app.currentWorkbook()
	ref := func(book string) reconcileSourceRef { return reconcileRef(t, a, book, "wb:账", 1) }
	req := versionRequest{PrimaryBook: sourceBookID(l), WorkbookKey: l.Fingerprint, LeftBook: sourceBookID(l), RightBook: sourceBookID(r), LeftSheet: "账", RightSheet: "账",
		Alignment: &versionAlignment{Left: ref(sourceBookID(l)), Right: ref(sourceBookID(r)), Keys: []reconcile.Pair{{Left: 1, Right: 1}}}}
	start := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/versions", req))
	job := awaitReconcileExport(t, a, start.JobID)
	f, err := os.Open(job.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]int{}
	for _, r := range rows[1:] {
		found[r[0]]++
		switch r[0] {
		case "业务行移位":
			if r[14] != "3" || r[15] != "6" || r[16] != `["B"]` {
				t.Fatal(r)
			}
		case "公式":
			if r[14] != "B2" || r[15] != "B3" || r[16] != `["001"]` || r[5] != r[9] {
				t.Fatal(r)
			}
		}
	}
	for k, n := range map[string]int{"业务行移位": 1, "新增业务行": 1, "删除业务行": 1, "新增单元格": 2, "删除单元格": 2, "公式": 1, "重复主键待核": 4, "无效主键待核": 2} {
		if found[k] != n {
			t.Fatal(k, found, rows)
		}
	}
}

func TestVersionReportKeepsFormulaTypeCacheAndSparseCoordinates(t *testing.T) {
	isolateTestStorage(t)
	const prefix = `<sheetData>`
	const suffix = "</sheetData>"
	left := editWorkbookFixture(t, map[string]string{"台账": prefix + `<row r="1"><c r="A1"><f>1+1</f><v>2</v></c><c r="B1" t="inlineStr"><is><t>001</t></is></c><c r="C1"><f>A1*2</f></c></row>` + suffix, "旧表": prefix + suffix}, "", "")
	right := editWorkbookFixture(t, map[string]string{"台账": prefix + `<row r="1"><c r="A1"><v>2</v></c><c r="B1"><v>1</v></c><c r="C1"><f>A1*2</f><v>4</v></c></row><row r="1000000"><c r="D1000000"><v>9</v></c></row>` + suffix}, "", "")
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(left); err != nil {
		t.Fatal(err)
	}
	id, err := a.OpenPathForLaunch(right)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := a.sessionFor(id)
	l, r := a.currentWorkbook(), b.app.currentWorkbook()
	req := versionRequest{PrimaryBook: sourceBookID(l), WorkbookKey: l.Fingerprint, LeftBook: sourceBookID(l), RightBook: sourceBookID(r)}
	start := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/versions", req))
	j := awaitReconcileExport(t, a, start.JobID)
	data, err := os.ReadFile(j.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][]string{}
	removed := false
	for _, row := range rows {
		m[row[3]] = row
		if row[0] == "删除工作表" && row[1] == "旧表" {
			removed = true
		}
	}
	if !removed || len(rows) > 12 || m["A1"][0] != "公式" || m["A1"][6] != "'=1+1" || m["A1"][5] != m["A1"][9] || !strings.Contains(m["B1"][0], "源类型") || m["B1"][5] != "001" || !strings.Contains(m["C1"][0], "缓存状态") || m["D1000000"][0] != "新增单元格" {
		t.Fatal(rows)
	}
	view := decodeResponse[struct{ URL string }](t, reconcilePost(t, a, "/api/reconcile/report/open", map[string]string{"id": j.ID}))
	child, err := a.sessionFor(strings.TrimPrefix(view.URL, "/?session="))
	if err != nil || child.app.currentWorkbook().Format != "csv" {
		t.Fatal(view, err)
	}
}
