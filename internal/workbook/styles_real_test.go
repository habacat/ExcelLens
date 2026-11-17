package workbook

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestRealStylePaletteSmoke(t *testing.T) {
	path := os.Getenv("EXCELLENS_REAL_XLSM")
	if path == "" {
		t.Skip("requires local workbook")
	}
	before := time.Now()
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	styled, filled, colored, borders := 0, 0, 0, 0
	for _, s := range wb.VisualStyles {
		if s.Color != "" || s.Background != "" || s.Bold || s.Italic || s.Underline || s.BorderTop != "" {
			styled++
		}
		if s.Background != "" {
			filled++
		}
		if s.Color != "" {
			colored++
		}
		if s.BorderTop != "" || s.BorderBottom != "" || s.BorderRight != "" || s.BorderLeft != "" {
			borders++
		}
	}
	b, err := json.Marshal(wb.VisualStyles)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("opened=%s sheets=%d styles=%d styled=%d filled=%d colored=%d bordered=%d paletteBytes=%d", time.Since(before), len(wb.Sheets), len(wb.VisualStyles), styled, filled, colored, borders, len(b))
	if len(wb.Sheets) < 20 || filled < 1 || colored < 1 {
		t.Fatalf("real workbook styles missing: %+v", wb.VisualStyles)
	}
}
