package server

import (
	"encoding/json"
	"excellens/internal/calculator"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeConditionalAndLookupOnLargeImportedLedger(t *testing.T) {
	isolateTestStorage(t)
	const size = 30000
	var data strings.Builder
	data.WriteString("机构,金额,编码\n")
	count := 0
	for i := 0; i < size; i++ {
		group := i % 7
		if group == 1 {
			count++
		}
		fmt.Fprintf(&data, "机构%d,0.10,000%06d\n", group, i)
	}
	path := filepath.Join(t.TempDir(), "ledger.csv")
	if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := app.currentWorkbook()
	if _, err = wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	sheet := wb.Sheets[0].Name
	call := func(expression string) calculator.Result {
		body, _ := json.Marshal(map[string]string{"expression": expression, "workbookKey": wb.Fingerprint, "sheet": sheet})
		return decodeResponse[calculator.Result](t, featureCall(t, app, "POST", "/api/calculator/formula", string(body)))
	}
	for expression, want := range map[string]string{`=COUNTIF(A:A,"机构1")`: fmt.Sprint(count), `=SUMIF(A:A,"机构1",B:B)`: big.NewRat(int64(count), 10).RatString(), `=SUMIFS(B:B,A:A,"机构1",B:B,">0")`: big.NewRat(int64(count), 10).RatString(), `=AVERAGEIF(A:A,"机构1",B:B)`: "1/10", `=XLOOKUP("机构1",A:A,C:C)`: "000000001", `=INDEX(C:C,3)`: "000000001", `=SUM(B2:B30001)`: "3000", `=SUM(INDEX(A2:C30001,0,2))`: "3000"} {
		r := call(expression)
		if r.Kind == "number" {
			got, err := calculator.Canonical(r.Value)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := calculator.Canonical(want)
			if err != nil || got.Cmp(expected) != 0 {
				t.Fatal(expression, r, want)
			}
		} else if r.Display != want {
			t.Fatal(expression, r, want)
		}
	}
	r := call(`=SUMPRODUCT((A2:A30001="机构1")*B2:B30001)`)
	expected := big.NewRat(int64(count), 10)
	got, err := calculator.Canonical(r.Value)
	if err != nil || got.Cmp(expected) != 0 {
		t.Fatal(r, err)
	}
	original, err := os.ReadFile(path)
	if err != nil || string(original) != data.String() {
		t.Fatal("source ledger changed")
	}
	t.Logf("30000 imported rows: %d matches, exact conditional sum %s; whole-column references, lookup identifiers, INDEX range, SUMPRODUCT and read-only source passed", count, expected.RatString())
}
