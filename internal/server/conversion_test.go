package server

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/calculator"
)

func TestCalculatorDateSuggestionDoesNotChangeResult(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	for _, tc := range []struct{ expression, kind string }{
		{`="2026/1/2"`, "date"},
		{`="09:05:12.5"`, "time"},
		{`="2026-01-02 09:05"`, "datetime"},
		{`="01/02/2026"`, ""},
		{`="00001234"`, ""},
		{`="20261111"`, ""},
		{`=1+1`, ""},
	} {
		body, _ := json.Marshal(map[string]string{"expression": tc.expression})
		response := featureCall(t, app, "POST", "/api/calculator/formula", string(body))
		var got struct {
			calculator.Result
			Suggestions []conversionSuggestion `json:"suggestions"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &got) != nil {
			t.Fatal(response.Code, response.Body.String())
		}
		if tc.kind == "" {
			if len(got.Suggestions) != 0 {
				t.Fatal("unsuitable result received a suggestion", got)
			}
		} else if len(got.Suggestions) != 1 || got.Suggestions[0].Kind != tc.kind || got.Kind != "text" {
			t.Fatal("suggestion changed the original result", got)
		}
	}
}

func TestManualConversionThroughIndexedSourceAndHTTP(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "dates.csv")
	original := []byte("日期,时间,编号\n2026/1/2,09:05:12.5,000001\n01/02/2026,14:30,000002\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	q := `{"source":"wb:数据","headerRow":1,"rows":["D_date"],"aggregate":"count","transforms":[{"id":"D_date","name":"转换日期","source":"C1","op":"date","arg":"auto"},{"id":"D_time","name":"转换时间","source":"C2","op":"time","arg":"auto"}],"filters":[{"field":"D_time","op":"between","value":"09:00..12:00"}]}`
	started := decodeResponse[struct {
		JobID string `json:"jobId"`
	}](t, analysisRequest(t, a, "POST", "/api/analysis/query/start", q))
	got := awaitAnalysis(t, a, started.JobID)
	if got.Scanned != 2 || got.Included != 1 || got.InvalidConversions != 1 || len(got.Rows) != 1 || got.Rows[0].Keys[0] != "2026-01-02" {
		t.Fatal(got)
	}
	calculate := func(expression string) struct {
		calculator.Result
		Suggestions []conversionSuggestion `json:"suggestions"`
	} {
		body, _ := json.Marshal(map[string]string{"expression": expression, "workbookKey": wb.Fingerprint, "sheet": "数据"})
		return decodeResponse[struct {
			calculator.Result
			Suggestions []conversionSuggestion `json:"suggestions"`
		}](t, featureCall(t, a, "POST", "/api/calculator/formula", string(body)))
	}
	raw := calculate("=A2")
	if raw.Kind != "text" || raw.Value != "2026/1/2" || len(raw.Suggestions) != 1 || raw.Suggestions[0].Kind != "date" {
		t.Fatal(raw)
	}
	date := calculate("=DATEVALUE(A2)")
	if date.Kind != "number" || date.Value != "46024" {
		t.Fatal(date)
	}
	clock := calculate("=TIMEVALUE(B2)")
	number, err := calculator.Canonical(clock.Value)
	if err != nil || number.Cmp(big.NewRat(65425, 172800)) != 0 {
		t.Fatal("time conversion lost its half second", clock, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) || calculate("=A2").Kind != "text" {
		t.Fatal("explicit conversion changed source bytes or its indexed type", err)
	}
}
