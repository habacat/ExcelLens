package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/calculator"
)

// Exercise the panel's real resolver, including source types, cached errors,
// empty cells, used extents and the workbook's date system.
func TestCalculatorExcelReference(t *testing.T) {
	isolateTestStorage(t)
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("testdata", "calculator-native", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	var cases struct{ Functions, Boundaries, Dates map[string]string }
	var native struct {
		Inputs  map[string]string
		Systems map[string]map[string][2]string
	}
	if err := json.Unmarshal([]byte(read("cases.json")), &cases); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(read("excel.json")), &native); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cases.json", "sheet.xml"} {
		if fmt.Sprintf("%x", sha256.Sum256([]byte(read(name)))) != native.Inputs[name] {
			t.Fatal("refresh Excel reference for", name)
		}
	}
	for _, spec := range calculator.Functions() {
		if cases.Functions[spec.Name] == "" {
			t.Fatalf("missing Excel case for %s", spec.Name)
		}
	}
	for _, system := range []string{"1900", "1904"} {
		t.Run(system, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reference.xlsx")
			writeZipArchive(t, path, map[string]string{
				"xl/workbook.xml":            fmt.Sprintf(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><workbookPr date1904="%t"/><sheets><sheet name="First" sheetId="1" r:id="rId1"/></sheets></workbook>`, system == "1904"),
				"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
				"xl/worksheets/sheet1.xml":   read("sheet.xml"),
			})
			app := prepareFeatureApp(t, path)
			defer app.Close()
			groups := []map[string]string{cases.Dates}
			if system == "1900" {
				groups = append(groups, cases.Functions, cases.Boundaries)
			}
			for _, group := range groups {
				for name, expr := range group {
					t.Run(name, func(t *testing.T) {
						want, ok := native.Systems[system][name]
						if !ok {
							t.Fatal("missing Excel result")
						}
						nativeValue := want
						if override, known := panelDifferences[name]; known {
							if want != override.excel {
								t.Fatalf("native result changed for recorded difference: %v", want)
							}
							want = override.panel
						}
						body, _ := json.Marshal(map[string]string{"expression": expr, "workbookKey": app.currentWorkbook().Fingerprint, "sheet": "First"})
						res := rawArchiveCall(app, "POST", "/api/calculator/formula", string(body))
						var got calculator.Result
						if res.Code == 422 && want[0] == "error" {
							var e struct{ Error string }
							if json.Unmarshal(res.Body.Bytes(), &e) == nil && strings.HasPrefix(e.Error, want[1]) {
								return
							}
						} else if res.Code == 200 && json.Unmarshal(res.Body.Bytes(), &got) == nil && got.Kind == want[0] {
							if want[0] != "number" && got.Display == want[1] {
								return
							}
							if want[0] == "number" {
								actual, e1 := calculator.Canonical(got.Value)
								expected, e2 := calculator.Canonical(want[1])
								if e1 == nil && e2 == nil {
									if actual.Cmp(expected) == 0 {
										return
									}
									x, _ := actual.Float64()
									y, _ := expected.Float64()
									tolerance := 2e-12 * math.Max(1, math.Abs(y))
									if name == "XIRR" {
										tolerance = 1e-8
									} // native iterative stopping threshold on these cash flows
									if math.Abs(x-y) <= tolerance {
										return
									}
								}
							}
						}
						t.Fatalf("%s Excel %v, expected %v, panel HTTP %d %s", expr, nativeValue, want, res.Code, res.Body)
					})
				}
			}
		})
	}
}

var panelDifferences = map[string]struct{ excel, panel [2]string }{
	"AVG":                {[2]string{"error", "#NAME?"}, [2]string{"number", "3"}},
	"full-column-blanks": {[2]string{"number", "1048574"}, [2]string{"number", "4"}},
	"binary-lookup":      {[2]string{"text", "北京东"}, [2]string{"error", "#VALUE!"}},
}
