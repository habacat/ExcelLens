package workbook

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func sourceMetadataWorkbook(t testing.TB, worksheet, styles string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "presentation.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, value := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   worksheet, "xl/styles.xml": styles,
	} {
		if value == "" {
			continue
		}
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestSourceMetadataCapturesBlankHiddenOutlineRowsAndConditionalByteRangesWithoutChangingExtent(t *testing.T) {
	isolateTestStorage(t)
	conditional := `<conditionalFormatting sqref="A1:A8"><cfRule type="cellIs" priority="1" dxfId="0" operator="greaterThan"><formula>100</formula></cfRule></conditionalFormatting>`
	x := `<worksheet><sheetPr><outlinePr summaryBelow="0" summaryRight="1"/></sheetPr><sheetFormatPr zeroHeight="1"/><cols><col min="2" max="4" hidden="1" outlineLevel="2" collapsed="1" width="0"/><col min="3" max="3" hidden="0" width="8"/></cols><sheetData><row r="1" hidden="0"><c r="A1"><v>1</v></c></row><row r="8" hidden="1" outlineLevel="1"/><row r="9" hidden="1" outlineLevel="1"/><row r="10" collapsed="1" ht="0"/></sheetData>` + conditional + `</worksheet>`
	file := sourceMetadataWorkbook(t, x, "")
	before, _ := os.ReadFile(file)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.RowCount() != 1 || idx.MaxRow != 1 || idx.MaxCol != 1 {
		t.Fatal("presentation-only rows extended values", idx)
	}
	layout, err := w.SourceLayout(w.Sheets[0], idx)
	if err != nil {
		t.Fatal(err)
	}
	if !layout.ZeroHeight || layout.SummaryBelow == nil || *layout.SummaryBelow || layout.SummaryRight == nil || !*layout.SummaryRight || layout.ConditionalNotice != "" || layout.StateNotice != "" {
		t.Fatal(layout)
	}
	if !reflect.DeepEqual(layout.RowStates, []SourceAxisRange{{Min: 1, Max: 1, HiddenSet: true}, {Min: 8, Max: 9, Hidden: true, HiddenSet: true, Level: 1}, {Min: 10, Max: 10, Collapsed: true, ZeroSize: true}}) {
		t.Fatal("hidden/empty rows lost", layout.RowStates)
	}
	if len(layout.ColStates) != 3 || layout.ColStates[0].Min != 2 || layout.ColStates[0].Max != 2 || layout.ColStates[0].Level != 2 || !layout.ColStates[0].Hidden || layout.ColStates[1].Min != 3 || layout.ColStates[1].Hidden || !layout.ColStates[2].ZeroSize {
		t.Fatal("column overlap did not override every attribute", layout.ColStates)
	}
	if len(layout.ConditionalBlocks) != 1 || string([]byte(x)[layout.ConditionalBlocks[0].Start:layout.ConditionalBlocks[0].End]) != conditional {
		t.Fatal("conditional block did not refer to unchanged cached XML", layout.ConditionalBlocks)
	}
	if err := os.Remove(w.sourceLayoutPath(w.Sheets[0])); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{2, 3} {
		legacy := sourceLayoutCache{Version: version, Fingerprint: w.Fingerprint, Sheet: w.Sheets[0].Path, Layout: SourceLayout{DefaultRow: 20, DefaultCol: 64}}
		legacy.Checksum = legacy.checksum()
		raw, _ := json.Marshal(legacy)
		if err := os.WriteFile(strings.Replace(w.sourceLayoutPath(w.Sheets[0]), "-v"+strconv.Itoa(sourceLayoutCacheVersion)+"-", "-v"+strconv.Itoa(version)+"-", 1), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := reopened.SourceLayout(reopened.Sheets[0], idx)
	if err != nil || !reflect.DeepEqual(layout, warm) {
		t.Fatal("old-cache migration lost metadata-only rows or tail rules", warm, err)
	}
	second, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := second.SourceLayout(second.Sheets[0], idx)
	if err != nil || !reflect.DeepEqual(warm, cached) {
		t.Fatal("current cache changed metadata", cached, err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("metadata reading modified source")
	}
}

func TestSourceMetadataIgnoresForeignNestedRulesAndRetainsExtensionAndInvalidStates(t *testing.T) {
	for _, tc := range []struct {
		xml                string
		blocks             int
		conditional, state bool
	}{
		{`<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><extra><conditionalFormatting sqref="A1"/></extra><t>value</t></is></c></row></sheetData></worksheet>`, 0, false, false},
		{`<worksheet><sheetData/><extLst><ext><x14:conditionalFormatting xmlns:x14="urn:extension"/></ext></extLst></worksheet>`, 0, true, false},
		{`<worksheet><cols><col min="1" max="1" outlineLevel="8"/></cols><sheetData><row r="1" hidden="maybe"/></sheetData></worksheet>`, 0, false, true},
	} {
		var layout SourceLayout
		if _, _, _, _, _, err := scanWorksheetWithSharedHint(strings.NewReader(tc.xml), int64(len(tc.xml)), nil, nil, nil, nil, &layout); err != nil {
			t.Fatal(err)
		}
		if len(layout.ConditionalBlocks) != tc.blocks || (layout.ConditionalNotice != "") != tc.conditional || (layout.StateNotice != "") != tc.state {
			t.Fatal(layout, tc)
		}
	}
}

func TestSourceLayoutMigrationCancellationAndFailedPublicationPreserveTheOldTarget(t *testing.T) {
	isolateTestStorage(t)
	file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`, "")
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readOOXMLSourceLayoutContext(ctx, idx); err != context.Canceled {
		t.Fatal("cancelled migration completed", err)
	}
	path := w.sourceLayoutPath(w.Sheets[0])
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	w.rememberSourceLayout(w.Sheets[0], SourceLayout{DefaultRow: 20, DefaultCol: 64})
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatal("failed cache publication removed the old target", info, err)
	}
}

func TestSourceMetadataKeepsZeroDefaultDimensionsAndInvalidSizeCauses(t *testing.T) {
	isolateTestStorage(t)
	worksheet := `<worksheet><sheetFormatPr defaultRowHeight="0" defaultColWidth="0"/><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`
	file := sourceMetadataWorkbook(t, worksheet, "")
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertZero := func(layout SourceLayout, err error) {
		t.Helper()
		if err != nil || !layout.DefaultRowZeroSize || !layout.DefaultColZeroSize || layout.DefaultRow != 0 || layout.DefaultCol != 0 || layout.StateNotice != "" || !layout.valid() {
			t.Fatal("zero source defaults became ordinary sizes", layout, err)
		}
	}
	assertZero(w.SourceLayout(w.Sheets[0], idx))
	if err := os.Remove(w.sourceLayoutPath(w.Sheets[0])); err != nil {
		t.Fatal(err)
	}
	for range 2 { // first rebuilds from cached XML, second reads the new sidecar
		reopened, err := OpenWorkbook(file)
		if err != nil {
			t.Fatal(err)
		}
		assertZero(reopened.SourceLayout(reopened.Sheets[0], idx))
	}
	for _, raw := range []string{"-1", "NaN", "INF", "oops"} {
		for _, fragment := range []string{
			`<sheetFormatPr defaultRowHeight="` + raw + `"/>`,
			`<sheetFormatPr defaultColWidth="` + raw + `"/>`,
			`<cols><col min="1" max="1" width="` + raw + `" hidden="1"/></cols>`,
			`<sheetData><row r="1" ht="` + raw + `" hidden="1"/></sheetData>`,
		} {
			var layout SourceLayout
			xml := `<worksheet>` + fragment + `</worksheet>`
			if _, _, _, _, _, err := scanWorksheetWithSharedHint(strings.NewReader(xml), int64(len(xml)), nil, nil, nil, nil, &layout); err != nil {
				t.Fatal(err)
			}
			if layout.StateNotice == "" || layout.DefaultRowZeroSize || layout.DefaultColZeroSize {
				t.Fatal("invalid size became a successful default/zero", fragment, layout)
			}
			for _, ranges := range [][]SourceAxisRange{layout.RowStates, layout.ColStates} {
				for _, state := range ranges {
					if !state.Hidden || state.ZeroSize {
						t.Fatal("invalid size discarded known visibility", fragment, state)
					}
				}
			}
		}
	}
}

func TestSourceLayoutConcurrentMigrationAndCancelledWaiter(t *testing.T) {
	isolateTestStorage(t)
	file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row><row r="99" hidden="1"/></sheetData></worksheet>`, "")
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.sourceLayoutMu.Lock()
	w.sourceLayouts = nil
	w.sourceLayoutMu.Unlock()
	if err := os.Remove(w.sourceLayoutPath(w.Sheets[0])); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			layout, err := w.SourceLayoutContext(context.Background(), w.Sheets[0], idx)
			if err != nil || len(layout.RowStates) != 1 || layout.RowStates[0].Min != 99 || !layout.RowStates[0].Hidden {
				t.Error("concurrent migration lost layout", layout, err)
			}
		})
	}
	group.Wait()
	// Hold another sheet's loading slot open, as while a large legacy XML
	// migration is in progress. Waiting requests must keep their cancellation.
	blocked := SheetMeta{Name: "blocked", Path: "blocked"}
	w.sourceLayoutMu.Lock()
	w.sourceLayoutLoading[blocked.Path] = &sourceLayoutLoad{done: make(chan struct{})}
	w.sourceLayoutMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := w.SourceLayoutContext(ctx, blocked, idx); err != context.DeadlineExceeded {
		t.Fatal("waiting layout request lost cancellation", err)
	}
	if _, err := w.SourceLayoutContext(context.Background(), w.Sheets[0], idx); err != nil {
		t.Fatal("blocked migration held another sheet's cached layout", err)
	}
}

func TestSourceLayoutBeforeIndexAvailabilityDoesNotMemoizeAnEmptyLayout(t *testing.T) {
	isolateTestStorage(t)
	file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row><row r="50" hidden="1"/></sheetData></worksheet>`, "")
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(w.sourceLayoutPath(w.Sheets[0])); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if layout, err := reopened.SourceLayout(reopened.Sheets[0], nil); err != nil || len(layout.RowStates) != 0 {
		t.Fatal("unavailable index unexpectedly performed a migration", layout, err)
	}
	loaded := reopened.GetCachedIndex(reopened.Sheets[0].Path)
	if loaded == nil {
		t.Fatal("persisted value index missing")
	}
	layout, err := reopened.SourceLayout(reopened.Sheets[0], loaded)
	if err != nil || len(layout.RowStates) != 1 || layout.RowStates[0].Min != 50 || !layout.RowStates[0].Hidden || loaded.MaxRow != idx.MaxRow {
		t.Fatal("early layout request permanently hid source metadata", layout, err)
	}
	second, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := second.SourceLayout(second.Sheets[0], nil)
	if err != nil || !reflect.DeepEqual(layout, cached) {
		t.Fatal("valid sidecar was not readable before index attachment", cached, err)
	}
}

func TestSourceLayoutIgnoresDimensionTagsInsideInlineExtensionsDuringIndexAndMigration(t *testing.T) {
	isolateTestStorage(t)
	xml := `<worksheet><sheetFormatPr defaultRowHeight="15"/><cols><col min="1" max="1" width="8"/></cols><sheetData><row r="1"><c r="A1" t="inlineStr"><is><extra><sheetFormatPr defaultRowHeight="30"/><col min="1" max="1" width="99"/><mergeCell ref="A1:Z900"/></extra><t>value</t></is></c></row></sheetData></worksheet>`
	file := sourceMetadataWorkbook(t, xml, "")
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertLayout := func(layout SourceLayout, err error) {
		t.Helper()
		if err != nil || layout.DefaultRow != 20 || layout.Cols[1] != 56 || len(layout.Merges) != 0 || len(layout.RowStates) != 0 || idx.MaxRow != 1 || idx.MaxCol != 1 {
			t.Fatal("inline metadata polluted worksheet layout", layout, err)
		}
	}
	assertLayout(w.SourceLayout(w.Sheets[0], idx))
	if err := os.Remove(w.sourceLayoutPath(w.Sheets[0])); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	assertLayout(reopened.SourceLayout(reopened.Sheets[0], idx))
}
