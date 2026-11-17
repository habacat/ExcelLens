package server

import (
	"encoding/json"
	"excellens/internal/workbook"
	"net/http"
	"testing"
)

func TestVisualStylesAPI(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	// A palette is a per-workbook payload, independent of sheet indexing.
	a.mu.Lock()
	a.wb = &workbook.Workbook{VisualStyles: []workbook.VisualStyle{
		{Color: "#000000", FontName: "宋体"},
		{Color: "#FFFFFF", Background: "#205080", Bold: true, BorderBottom: "1px solid #000000"},
	}}
	a.mu.Unlock()
	res := featureCall(t, a, "GET", "/api/styles", "")
	if res.Code != http.StatusOK {
		t.Fatalf("styles status %d %s", res.Code, res.Body.String())
	}
	var data struct {
		Styles []workbook.VisualStyle `json:"styles"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Styles) != 2 || data.Styles[1].Background != "#205080" || data.Styles[1].Color != "#FFFFFF" {
		t.Fatalf("bad palette payload %+v", data.Styles)
	}
}
