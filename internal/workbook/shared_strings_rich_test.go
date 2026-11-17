package workbook

import (
	"archive/zip"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func richWorkbookFile(t *testing.T, shared string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "rich.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	out := zip.NewWriter(f)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="数据" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="s"><v>2</v></c></row></sheetData></worksheet>`,
		"xl/sharedStrings.xml":       shared,
		"xl/styles.xml":              `<styleSheet><fonts count="1"><font><name val="宋体"/><sz val="11"/></font></fonts><cellXfs count="1"><xf numFmtId="0" fontId="0"/></cellXfs><colors><indexedColors><rgbColor rgb="FF7F1234"/></indexedColors></colors></styleSheet>`,
	}
	for name, value := range parts {
		entry, err := out.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

const sharedRichFixture = `<sst><si><r><rPr><b/></rPr><t>001</t></r></si><si><t>普通&amp;原文</t></si><si><r><rPr><color indexed="0"/><i val="0"/></rPr><t>甲😀</t></r><rPh><t>注音</t></rPh></si></sst>`

func TestWorkbookSharedRichColdAndWarmOpenPreserveValuesPaletteAndMappedLifetime(t *testing.T) {
	isolateTestStorage(t)
	file := richWorkbookFile(t, sharedRichFixture)
	before, _ := os.ReadFile(file)
	first, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRichStore(first.richShared)
	if !reflect.DeepEqual(first.Shared, []string{"001", "普通&原文", "甲😀"}) || first.richShared == nil || first.RichTextNotice() != "" || !first.HasMappedIndexes() {
		t.Fatal(first.Shared, first.RichTextNotice())
	}
	value, err := first.richShared.lookup(2, first.Shared[2])
	if err != nil || value.Runs[0].Font.Color != "#7F1234" || value.Runs[0].Font.Italic == nil || *value.Runs[0].Font.Italic {
		t.Fatal("palette was not loaded before rich strings", value, err)
	}
	cacheBefore, _ := os.ReadFile(first.sharedRichPath())
	statBefore, _ := os.Stat(first.sharedRichPath())
	second, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRichStore(second.richShared)
	cacheAfter, _ := os.ReadFile(second.sharedRichPath())
	statAfter, _ := os.Stat(second.sharedRichPath())
	if first.Fingerprint != second.Fingerprint || !reflect.DeepEqual(first.Shared, second.Shared) || string(cacheBefore) != string(cacheAfter) || !statBefore.ModTime().Equal(statAfter.ModTime()) || second.RichTextNotice() != "" {
		t.Fatal("warm load rewrote or rebound the immutable rich cache")
	}
	closeRichStore(first.richShared)
	first.richShared = nil
	if value, err := second.richShared.lookup(0, second.Shared[0]); err != nil || value == nil || value.Runs[0].Font.Bold == nil || !*value.Runs[0].Font.Bold {
		t.Fatal("releasing another owner invalidated active shared styles", err)
	}
	index, err := second.BuildIndex(second.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := second.ReadRow(index, 0, 1, 3)
	if err != nil || len(row.Cells) != 3 || row.Cells[0].Raw != "001" || row.Cells[0].Type != "s" || row.Cells[2].Raw != "甲😀" {
		t.Fatal("source cells changed", row, err)
	}
	after, _ := os.ReadFile(file)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("style loading modified the workbook")
	}
}

func TestSharedRichFastFallbackDiscardsPartialCaptureAndRetainsCorrectIndices(t *testing.T) {
	isolateTestStorage(t)
	input := strings.Replace(sharedRichFixture, "</sst>", "<extLst/></sst>", 1)
	observed := 0
	probe := &sharedStringPresentation{read: (&Workbook{}).readRichTextStyled, observe: func(int, string, *RichText) { observed++ }}
	if _, ok := decodeSharedStringsFastWithPresentation([]byte(input), probe); ok || observed == 0 {
		t.Fatal("fixture did not force a late fast-path fallback", ok, observed)
	}
	w, err := OpenWorkbook(richWorkbookFile(t, input))
	if err != nil {
		t.Fatal(err)
	}
	defer closeRichStore(w.richShared)
	if w.RichTextNotice() != "" || w.richShared == nil || w.richShared.count != 2 || !reflect.DeepEqual(w.Shared, []string{"001", "普通&原文", "甲😀"}) {
		t.Fatal("partial fast capture survived strict fallback", w.Shared, w.RichTextNotice())
	}
	if value, err := w.richShared.lookup(0, w.Shared[0]); err != nil || value == nil || value.Runs[0].Font.Bold == nil || !*value.Runs[0].Font.Bold {
		t.Fatal(value, err)
	}
	if value, err := w.richShared.lookup(1, w.Shared[1]); err != nil || value != nil {
		t.Fatal("plain entry got a different string's font", value, err)
	}
}

func TestSharedRichCacheFailureKeepsCompleteSourceAndReportsSeparateNotice(t *testing.T) {
	isolateTestStorage(t)
	file := richWorkbookFile(t, sharedRichFixture)
	archive, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := fingerprintZip(archive.File)
	_ = archive.Close()
	blocked := filepath.Join(CacheRoot(), fingerprint, "rich-shared-v1.bin")
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal("metadata publication hid readable source values", err)
	}
	if w.richShared != nil || w.RichTextNotice() == "" || !reflect.DeepEqual(w.Shared, []string{"001", "普通&原文", "甲😀"}) {
		t.Fatal(w.Shared, w.RichTextNotice())
	}
	if stat, err := os.Stat(blocked); err != nil || !stat.IsDir() {
		t.Fatal("failed cache publication removed its existing target", err)
	}
	entries, err := os.ReadDir(w.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".rich-") {
			t.Fatal("failed cache retained partial files", entry.Name())
		}
	}
}

func TestSharedRichCorruptSidecarRebuildsAndInvalidXMLNeverPublishesPartialStyles(t *testing.T) {
	isolateTestStorage(t)
	file := richWorkbookFile(t, sharedRichFixture)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	path := w.sharedRichPath()
	closeRichStore(w.richShared)
	w.richShared = nil
	if err := os.WriteFile(path, []byte("corrupt-cache"), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRichStore(reopened.richShared)
	if reopened.richShared == nil || reopened.RichTextNotice() != "" {
		t.Fatal("source cache did not rebuild", reopened.RichTextNotice())
	}
	if _, err := reopened.richShared.lookup(2, reopened.Shared[2]); err != nil {
		t.Fatal(err)
	}
	broken := richWorkbookFile(t, strings.TrimSuffix(sharedRichFixture, "</sst>"))
	if _, err := OpenWorkbook(broken); err == nil {
		t.Fatal("truncated value XML was reported as complete")
	}
}

func TestPlainAndUnstyledSharedRunsCreateNoPresentationSidecar(t *testing.T) {
	isolateTestStorage(t)
	w, err := OpenWorkbook(richWorkbookFile(t, `<sst><si><t>001</t></si><si><r><t>未</t></r><r><t>设样式</t></r></si><si><t>普通</t></si></sst>`))
	if err != nil {
		t.Fatal(err)
	}
	if w.richShared != nil || w.RichTextNotice() != "" || w.HasMappedIndexes() {
		t.Fatal("plain shared strings created a presentation mapping")
	}
	if _, err := os.Stat(w.sharedRichPath()); !os.IsNotExist(err) {
		t.Fatal("plain shared strings created a sidecar", err)
	}
}
