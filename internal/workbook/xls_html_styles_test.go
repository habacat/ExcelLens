package workbook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHTMLXLSCommonStylesPreviewAndMetadataCache(t *testing.T) {
	isolateTestStorage(t)
	text := `<HTML><HEAD><STYLE>td {font-family:宋体;font-size:9pt;color:#000;vertical-align:middle;} /* title */ .head {font-weight:700;background:#E1F5FF;text-align:center;border:.5pt solid #000;} .amount {text-align:right;border:1px solid blue;}</STYLE></HEAD><table><tr><td class="head">金额</td><td class="head">账号</td></tr><tr><td class="amount" style="border-right:none;">1,234.500001</td><td x:str>00012345678901234567</td></tr></table></HTML>`
	path := filepath.Join(t.TempDir(), "styles.xls")
	_ = os.WriteFile(path, []byte(text), 0600)
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := wb.PreviewRows(context.Background(), wb.Sheets[0], 80, 40)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := wb.ReadRows(idx, 0, 2, 1, 40)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preview, rows) {
		t.Fatal("preview/index style IDs or values diverged")
	}
	header := wb.VisualStyles[rows[0].Cells[0].Style]
	if !header.Bold || header.Background != "#e1f5ff" || header.Horizontal != "center" || header.FontName != "宋体" || header.FontSize != 9 || header.BorderBottom == "" {
		t.Fatalf("header style %+v", header)
	}
	amount := wb.VisualStyles[rows[1].Cells[0].Style]
	if amount.Horizontal != "right" || amount.BorderRight != "" || amount.BorderLeft == "" || rows[1].Cells[0].Raw != "1,234.500001" || rows[1].Cells[1].Raw != "00012345678901234567" {
		t.Fatal("style conversion changed financial text", amount, rows[1].Cells)
	}
	started := time.Now()
	reopened, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("warm HTML metadata open %s", time.Since(started))
	if !reflect.DeepEqual(wb.VisualStyles, reopened.VisualStyles) {
		t.Fatal("cached style palette changed")
	}
	cachePath := filepath.Join(wb.CacheDir, "html-metadata-v4.json")
	bad := htmlWorkbookMetadata{Version: 4, Fingerprint: wb.Fingerprint, Names: []string{"bogus"}, Styles: htmlStyleMetadata{Palette: []VisualStyle{{}}, IDs: map[string]int{"bad": 65535}}}
	bad.Checksum = bad.checksum()
	raw, _ := json.Marshal(bad)
	_ = os.WriteFile(cachePath, raw, 0600)
	rebuilt, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wb.VisualStyles, rebuilt.VisualStyles) {
		t.Fatal("invalid metadata receipt was accepted instead of rebuilt")
	}
	// Valid JSON, valid palette IDs, but changed names/color must not silently
	// override source metadata. Re-checksumming another source is rejected too.
	for _, corrupt := range []func(*htmlWorkbookMetadata){
		func(m *htmlWorkbookMetadata) { m.Names[0] = "changed" },
		func(m *htmlWorkbookMetadata) { m.Styles.Palette[1].Color = "#ff0000" },
		func(m *htmlWorkbookMetadata) { m.Fingerprint = "other-source"; m.Checksum = m.checksum() },
	} {
		raw, err := os.ReadFile(cachePath)
		if err != nil {
			t.Fatal(err)
		}
		var m htmlWorkbookMetadata
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		corrupt(&m)
		raw, _ = json.Marshal(m)
		if err := os.WriteFile(cachePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := OpenWorkbook(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Sheets, wb.Sheets) || !reflect.DeepEqual(got.VisualStyles, wb.VisualStyles) {
			t.Fatal("corrupted metadata was accepted")
		}
	}
}
