package workbook

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The QA script generates each pair in Excel and removes machine metadata.
func TestXLSBExcelPair(t *testing.T) {
	dir := os.Getenv("EXCELLENS_XLSB_EXCEL_PAIRS")
	if dir == "" {
		dir = filepath.Join("testdata", "xlsb-excel")
		verifyXLSBExcelFixtures(t, dir)
	}
	cache := t.TempDir()
	t.Setenv("LOCALAPPDATA", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	for _, name := range []string{"fidelity2", "fidelity1904"} {
		t.Run(name, func(t *testing.T) {
			binaryPath, xmlPath := filepath.Join(dir, name+".xlsb"), filepath.Join(dir, name+".xlsx")
			original, err := os.ReadFile(binaryPath)
			if err != nil {
				t.Fatal(err)
			}
			w, err := OpenWorkbook(binaryPath)
			if err != nil {
				t.Fatal(err)
			}
			other, err := OpenWorkbook(xmlPath)
			if err != nil {
				t.Fatal(err)
			}
			if w.Date1904 != (name == "fidelity1904") || w.Date1904 != other.Date1904 || len(w.Sheets) != 3 || len(w.Sheets) != len(other.Sheets) {
				t.Fatal("workbook properties differ")
			}
			for i, sheet := range w.Sheets {
				if sheet.Name != other.Sheets[i].Name || sheet.State != other.Sheets[i].State {
					t.Fatal("sheet names or visibility differ", sheet, other.Sheets[i])
				}
			}
			if w.Sheets[2].State != "veryHidden" || !w.SourceInfo.ReadOnly {
				t.Fatal("very hidden sheet or source semantics lost")
			}
			meta := w.Sheets[0]
			pre, err := w.PreviewRows(t.Context(), meta, 5, 10)
			if err != nil || len(pre) != 5 || w.GetCachedIndex(meta.Path) != nil {
				t.Fatal("native preview must be a prefix without building the index", len(pre), err)
			}
			if _, err := os.Stat(filepath.Join(w.CacheDir, "strings.xlsb-sst")); !os.IsNotExist(err) {
				t.Fatal("native preview scanned the shared string table", err)
			}
			idx, err := w.BuildIndex(meta, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			xmlIdx, err := other.BuildIndex(other.Sheets[0], nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if idx.MaxRow != 1080 || idx.MaxRow != xmlIdx.MaxRow || idx.MaxCol != xmlIdx.MaxCol || idx.RowCount() != xmlIdx.RowCount() || !isExternalRows(idx.XMLPath) {
				t.Fatal("source extent, native path or sparse index differs", idx.MaxRow, idx.MaxCol, idx.RowCount())
			}
			read := RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true}
			rows, err := w.ReadRowsWithOptionsContext(t.Context(), idx, 0, 11, 1, 12, read)
			if err != nil {
				t.Fatal(err)
			}
			xmlRows, err := other.ReadRowsWithOptionsContext(t.Context(), xmlIdx, 0, 11, 1, 12, read)
			if err != nil {
				t.Fatal(err)
			}
			cells := xlsbTestCells(rows)
			xmlCells := xlsbTestCells(xmlRows)
			for ref, expected := range xmlCells {
				if expected.Raw == "" && !expected.HasFormula {
					continue
				}
				actual, ok := cells[ref]
				if !ok || actual.Col != expected.Col || actual.Style != expected.Style || !xlsbComparableRaw(actual, expected) || actual.Display != expected.Display && actual.Type != "n" {
					t.Errorf("%s value/style: %v versus %v", ref, actual, expected)
				}
			}
			want := map[string]string{"A2": "000012", "E2": "231.75", "E3": "TRUE", "E4": "000021-后", "E5": "#DIV/0!", "E9": "10", "F3": "-25"}
			for ref, value := range want {
				if got := cells[ref].Raw; got != value {
					t.Errorf("%s source value %q, want %q", ref, got, value)
				}
			}
			for ref, formula := range map[string]string{"D3": "'费率 表'!$B$2", "E2": "SUM(Ledger[Amount])", "E3": "IF(", "E4": "A4&", "E5": "1/0", "E9": "SUM({1,2;3,4})", "F3": "B2:B5*2"} {
				c := cells[ref]
				if !c.HasFormula || c.CacheMissing || !strings.Contains(c.Formula, formula) {
					t.Errorf("%s formula or cache: %v", ref, c)
				}
			}
			if cells["C2"].Display != xmlCells["C2"].Display || cells["L2"].Display != xmlCells["L2"].Display ||
				cells["A2"].Type != "s" || cells["E3"].Type != "b" || cells["E4"].Type != "str" || cells["E5"].Type != "e" ||
				cells["G4"].RichText == nil || len(cells["G4"].RichText.Runs) != 2 || cells["G4"].RichText.Runs[0].End != 5 ||
				w.VisualStyles[cells["G4"].Style].Color != "#DD3232" || cells["G4"].RichText.Runs[1].Font.Color != "#000000" {
				t.Fatal("dates, formula caches or rich text differ")
			}
			if style, expected := w.VisualStyles[cells["G6"].Style], other.VisualStyles[xmlCells["G6"].Style]; style != expected || !style.Bold || !style.Italic || !style.Wrap || style.Background == "" || style.BorderLeft == "" || style.Horizontal != "center" {
				t.Fatal("theme, fill, border or alignment differ", style, expected)
			}
			last, err := w.ReadRowsWithOptionsContext(t.Context(), idx, idx.RowCount()-1, 1, 4, 4, read)
			shared := xlsbTestCells(last)["D1080"]
			if err != nil || !shared.SharedFormula || shared.CacheMissing || shared.Raw != "135" || !strings.Contains(shared.Formula, "ROW()*0.125") {
				t.Fatal("last shared formula lost", shared, err)
			}
			verifyXLSBMetadata(t, w, meta, idx, rows)
			warm, err := OpenWorkbook(binaryPath)
			if err != nil || warm.GetCachedIndex(meta.Path) == nil {
				t.Fatal("native row/index cache was not reusable", err)
			}
			got, err := os.ReadFile(binaryPath)
			if err != nil || sha256.Sum256(got) != sha256.Sum256(original) {
				t.Fatal("source XLSB changed", err)
			}
		})
	}
}

func verifyXLSBExcelFixtures(t *testing.T, dir string) {
	t.Helper()
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{"fidelity2.xlsb": true, "fidelity2.xlsx": true, "fidelity1904.xlsb": true, "fidelity1904.xlsx": true}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !expected[fields[1]] {
			t.Fatalf("unexpected native fixture receipt %q", line)
		}
		name, digest := fields[1], fields[0]
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
			t.Fatalf("native fixture %s does not match its fixed digest: %v", name, err)
		}
		delete(expected, name)
	}
	if len(expected) != 0 {
		t.Fatalf("native fixture receipts missing: %v", expected)
	}
}

func xlsbTestCells(rows []RowData) map[string]CellData {
	out := make(map[string]CellData)
	for _, row := range rows {
		for _, cell := range row.Cells {
			out[cell.Ref] = cell
		}
	}
	return out
}

func xlsbComparableRaw(a, b CellData) bool {
	if a.Raw == b.Raw {
		return true
	}
	if a.Type != "n" || b.Type != "" && b.Type != "n" {
		return false
	}
	x, e1 := strconv.ParseFloat(a.Raw, 64)
	y, e2 := strconv.ParseFloat(b.Raw, 64)
	// Excel can round the decimal serialization of its saved binary double
	// by one ULP when it writes the corresponding OOXML cache.
	return e1 == nil && e2 == nil && (x == y || x == math.Nextafter(y, x))
}

func verifyXLSBMetadata(t *testing.T, w *Workbook, meta SheetMeta, idx *SheetIndex, rows []RowData) {
	t.Helper()
	ctx := context.Background()
	layout, err := w.SourceLayout(meta, idx)
	if err != nil || len(layout.Merges) != 1 || layout.Merges[0] != (CellMerge{Row: 6, Col: 7, EndRow: 7, EndCol: 9}) ||
		layout.FrozenRows <= 0 || layout.FrozenCols != 1 || layout.GridLines == nil || *layout.GridLines || len(layout.RowStates) == 0 {
		t.Fatal("geometry or freeze lost", layout, err)
	}
	hidden, err := w.SourceHiddenColumns(meta, idx)
	if err != nil || !reflect.DeepEqual(hidden, []HiddenColumnRange{{Min: 11, Max: 11}}) {
		t.Fatal("source column visibility lost", hidden, err)
	}
	rules, err := w.SourceDataRules(ctx, meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := rules.At(ctx, 12, 8, 0, 10)
	if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0].InlineList, []string{"低", "中", "高"}) {
		t.Fatal("validation list lost", page, err)
	}
	cond, err := w.SourceConditionalFormats(ctx, meta, idx)
	if err != nil || cond.Summary().Rules != 3 || cond.Summary().Pending != 2 {
		t.Fatal("conditional definitions lost", cond, err)
	}
	bar := cond.rules[1].source
	if bar.Type != "dataBar" || bar.Parameters["ext14-maxLength-1"] != "100" || bar.Parameters["color-5"] != "#638EC6" {
		t.Fatal("conditional data bar extension or color lost", bar)
	}
	notes, _, err := w.SheetNotes(ctx, meta)
	if err != nil || len(notes) != 1 || notes[0].Ref != "B4" || notes[0].Text != "同源 QA 批注" {
		t.Fatal("notes lost", notes, err)
	}
	images, err := w.SheetImages(meta)
	if err != nil || len(images) != 1 {
		t.Fatal("embedded image lost", images, err)
	}
	if err := w.DecorateHyperlinks(meta, rows, nil, 1, idx.MaxCol); err != nil {
		t.Fatal(err)
	}
	cells := xlsbTestCells(rows)
	if h := cells["G10"].Hyperlink; h == nil || h.Kind != "external" || h.URL != "https://example.com/" {
		t.Fatal("external hyperlink lost", h)
	}
	if h := cells["G11"].Hyperlink; h == nil || h.Kind != "internal" || h.Sheet != "费率 表" || h.Ref != "B2" {
		t.Fatal("internal hyperlink lost", h)
	}
	refs, err := w.SourcePrintSettings(ctx, meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := refs.Snapshot()
	if len(p.Names) != 2 || p.Names[0].Status != "ready" || p.Names[1].Status != "ready" || len(p.RowBreaks) != 1 ||
		p.RowBreaks[0].Index != 17 || p.Properties["pageSetup"]["orientation"].Value != "landscape" {
		t.Fatal("print definitions lost", p)
	}
	sheet := w.Sheets[1]
	tableIndex, err := w.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalogue, err := w.ConfirmCatalogue(ctx, sheet, tableIndex, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range catalogue.Items {
		if item.Name == "Ledger" && item.Status == "ready" && item.Table != nil && item.Table.Verified && len(item.Table.Columns) == 2 {
			return
		}
	}
	t.Fatal("verified native table definition lost", catalogue.Items)
}
