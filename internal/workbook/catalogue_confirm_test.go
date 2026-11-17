package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/formats"
)

func catalogueIndexedFixture(t *testing.T, parts map[string]string) (*Workbook, SheetMeta, *SheetIndex, string) {
	t.Helper()
	file := writeCatalogueZIPFixture(t, parts, false)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta, found := wb.FindSheet("原表")
	if !found {
		t.Fatal("missing fixture sheet")
	}
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return wb, meta, idx, file
}

func catalogueAssertConfirmed(t *testing.T, value Catalogue) {
	t.Helper()
	table := catalogueFind(t, value, "账表", "")
	if table.Status != "ready" || !table.Table.Verified || len(table.Areas) != 1 || table.Areas[0].Start != "B3" || table.Areas[0].End != "D9" {
		t.Fatal("real table declaration not confirmed", table)
	}
	if name := catalogueFind(t, value, "结构化", ""); name.Status != "ready" || name.Areas[0].Start != "C4" || name.Areas[0].End != "C8" {
		t.Fatal("structured name not resolved against confirmed data column", name)
	}
	if orphan := catalogueFind(t, value, "孤立关系", ""); orphan.Status != "invalid" || orphan.Table.Verified || len(orphan.Areas) != 0 {
		t.Fatal("orphan relationship became a real table", orphan)
	}
}

func TestCatalogueConfirmationCapturesDeclarationsOutsideTrimmedRowsAndReusesAttestedCacheWithoutBody(t *testing.T) {
	isolateTestStorage(t)
	parts := catalogueSourceParts()
	var tail strings.Builder
	for row := 10; row < 150_000; row++ {
		fmt.Fprintf(&tail, `<row r="%d"><c r="A%d" s="0"/></row>`, row, row)
	}
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `</sheetData>`, tail.String()+`</sheetData>`, 1)
	wb, meta, idx, file := catalogueIndexedFixture(t, parts)
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if idx.XMLSize < formats.CataloguePartBytes || idx.RowCount() != 1 || idx.RowAt(0).Number != 1 {
		t.Fatal("fixture did not exercise format-only rows after trimmed extent", idx.XMLSize, idx.RowCount())
	}
	value, err := wb.ReadCatalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	catalogueAssertConfirmed(t, value)
	if _, err := os.Stat(wb.worksheetCataloguePath(meta)); err != nil {
		t.Fatal("initial index did not publish independent declaration proof", err)
	}
	if wb.sourceLayouts[meta.Path].catalogue != nil {
		t.Fatal("transient XML headers retained in layout cache")
	}
	// Reopen and load the actual index first. Move only its private XML aside:
	// a valid proof must not reopen or rescan that body during confirmation.
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := reopened.GetCachedIndex(meta.Path)
	if reloaded == nil {
		t.Fatal("ordinary cached index missing")
	}
	aside := filepath.Join(filepath.Dir(idx.XMLPath), "body-held-aside.xml")
	if err := os.Rename(idx.XMLPath, aside); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(aside, idx.XMLPath)
	progressCalls := 0
	confirmed, err := reopened.ConfirmCatalogue(context.Background(), meta, reloaded, func(int64, int64, string) { progressCalls++ })
	if err != nil || progressCalls != 0 {
		t.Fatal("warm proof unexpectedly required worksheet body", err, progressCalls)
	}
	catalogueAssertConfirmed(t, confirmed)
	after, err := os.ReadFile(file)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("confirmation changed source bytes", err)
	}
}

func TestCatalogueDeclarationNamespaceCountAndPositionNeverAcceptExtensionOrMalformedTables(t *testing.T) {
	isolateTestStorage(t)
	for _, mode := range []string{"prefixed", "strict", "nested", "wrongNamespace", "wrongDataNamespace", "duplicate", "count", "duplicateID", "unqualifiedID", "beforeData", "truncatedRoot", "tail"} {
		t.Run(mode, func(t *testing.T) {
			parts := catalogueSourceParts()
			xml := parts["xl/worksheets/sheet1.xml"]
			switch mode {
			case "prefixed":
				xml = strings.NewReplacer(`<worksheet `, `<s:worksheet xmlns:s="`+catalogueTestNS+`" `, `</worksheet>`, `</s:worksheet>`, `<sheetData>`, `<s:sheetData>`, `</sheetData>`, `</s:sheetData>`, `<tableParts `, `<s:tableParts `, `</tableParts>`, `</s:tableParts>`, `<tablePart `, `<s:tablePart `).Replace(xml)
			case "strict":
				for name, content := range parts {
					parts[name] = strings.ReplaceAll(strings.ReplaceAll(content, catalogueTestNS, "http://purl.oclc.org/ooxml/spreadsheetml/main"), catalogueTestRel, "http://purl.oclc.org/ooxml/officeDocument/relationships")
				}
				xml = parts["xl/worksheets/sheet1.xml"]
			case "nested":
				xml = strings.Replace(xml, `<tableParts `, `<extLst><ext><tableParts `, 1)
				xml = strings.Replace(xml, `</tableParts>`, `</tableParts></ext></extLst>`, 1)
			case "wrongNamespace":
				xml = strings.Replace(xml, `<tableParts `, `<tableParts xmlns="urn:extension" `, 1)
			case "wrongDataNamespace":
				xml = strings.Replace(xml, `<sheetData>`, `<sheetData xmlns="urn:extension">`, 1)
			case "duplicate":
				xml = strings.Replace(xml, `</tableParts>`, `</tableParts><tableParts count="1"><tablePart r:id="t1"/></tableParts>`, 1)
			case "count":
				xml = strings.Replace(xml, `tableParts count="1"`, `tableParts count="2"`, 1)
			case "duplicateID":
				xml = strings.Replace(xml, `tableParts count="1"`, `tableParts count="2"`, 1)
				xml = strings.Replace(xml, `<tablePart r:id="t1"/>`, `<tablePart r:id="t1"/><tablePart r:id="t1"/>`, 1)
			case "unqualifiedID":
				xml = strings.Replace(xml, `tablePart r:id="t1"`, `tablePart id="t1"`, 1)
			case "beforeData":
				declaration := `<tableParts count="1"><tablePart r:id="t1"/></tableParts>`
				xml = strings.Replace(xml, declaration, "", 1)
				xml = strings.Replace(xml, `<sheetData>`, declaration+`<sheetData>`, 1)
			case "truncatedRoot":
				xml = strings.TrimSuffix(xml, `</worksheet>`)
			case "tail":
				xml += "invalid tail"
			}
			parts["xl/worksheets/sheet1.xml"] = xml
			wb, meta, idx, _ := catalogueIndexedFixture(t, parts)
			value, err := wb.ConfirmCatalogue(context.Background(), meta, idx, nil)
			if mode == "prefixed" || mode == "strict" {
				if err != nil {
					t.Fatal(err)
				}
				catalogueAssertConfirmed(t, value)
			} else if mode == "nested" {
				if err != nil {
					t.Fatal(err)
				}
				if table := catalogueFind(t, value, "账表", ""); table.Status != "invalid" || table.Table.Verified || len(table.Areas) > 0 {
					t.Fatal("nested extension manufactured a declaration", table)
				}
			} else if err == nil || len(value.Items) > 0 {
				t.Fatal("malformed source declaration returned a successful catalogue", value, err)
			}
		})
	}
}

func TestCatalogueConfirmationRequiresPreparedIndexAndSourceIdentityAtPublication(t *testing.T) {
	isolateTestStorage(t)
	parts := catalogueSourceParts()
	file := writeCatalogueZIPFixture(t, parts, false)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := wb.FindSheet("原表")
	if _, err := wb.ConfirmCatalogue(context.Background(), meta, nil, nil); err == nil || wb.GetCachedIndex(meta.Path) != nil {
		t.Fatal("confirmation implicitly built an index", err)
	}
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(wb.worksheetCataloguePath(meta)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx = reopened.GetCachedIndex(meta.Path)
	replacement := catalogueSourceParts()
	replacement["xl/workbook.xml"] = strings.Replace(replacement["xl/workbook.xml"], "$B$2", "$C$2", 1)
	replacementFile := writeCatalogueZIPFixture(t, replacement, false)
	replacementBytes, err := os.ReadFile(replacementFile)
	if err != nil {
		t.Fatal(err)
	}
	value, err := reopened.ConfirmCatalogue(context.Background(), meta, idx, func(int64, int64, string) {
		if err := os.WriteFile(file, replacementBytes, 0600); err != nil {
			t.Error(err)
		}
	})
	if err == nil || len(value.Items) != 0 {
		t.Fatal("source changed during confirmation but result published", value, err)
	}
	if _, err := os.Stat(reopened.worksheetCataloguePath(meta)); !os.IsNotExist(err) {
		t.Fatal("source-changing confirmation saved a proof", err)
	}
}

func TestCatalogueDeclarationsKeepMissingRelationsExplicitAndExcludeConfirmedOrphansFromNameBinding(t *testing.T) {
	isolateTestStorage(t)
	parts := catalogueSourceParts()
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `tableParts count="1"`, `tableParts count="2"`, 1)
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `<tablePart r:id="t1"/>`, `<tablePart r:id="t1"/><tablePart r:id="missing"/>`, 1)
	parts["xl/tables/orphan.xml"] = strings.ReplaceAll(parts["xl/tables/orphan.xml"], "孤立关系", "账表")
	wb, meta, idx, _ := catalogueIndexedFixture(t, parts)
	value, err := wb.ConfirmCatalogue(context.Background(), meta, idx, nil)
	if err != nil || value.Complete || len(value.Notices) == 0 {
		t.Fatal("missing declared relationship hidden", value, err)
	}
	if alias := catalogueFind(t, value, "结构化", ""); alias.Status != "ready" || alias.Areas[0].Start != "C4" {
		t.Fatal("confirmed orphan name shadowed the real table", alias)
	}
	for _, item := range value.Items {
		if item.Table != nil && item.Table.RelationID == "tOrphan" && (item.Status != "invalid" || item.Table.Verified || len(item.Areas) > 0) {
			t.Fatal("orphan became a real table", item)
		}
	}
}

func TestCatalogueMissingOrCorruptProofRebuildsOnceChecksFullCRCAndRejectsForeignIndex(t *testing.T) {
	isolateTestStorage(t)
	for _, mode := range []string{"missing", "version", "fingerprint", "sheet", "size", "sourceCRC", "checksum", "duplicateID", "XMLCorruption"} {
		t.Run(mode, func(t *testing.T) {
			wb, meta, idx, file := catalogueIndexedFixture(t, catalogueSourceParts())
			cachePath := wb.worksheetCataloguePath(meta)
			if mode == "missing" || mode == "XMLCorruption" {
				if err := os.Remove(cachePath); err != nil {
					t.Fatal(err)
				}
			} else {
				raw, err := os.ReadFile(cachePath)
				var cache worksheetCatalogueCache
				if err != nil || json.Unmarshal(raw, &cache) != nil {
					t.Fatal(err)
				}
				switch mode {
				case "version":
					cache.Version++
				case "fingerprint":
					cache.Fingerprint = "foreign"
				case "sheet":
					cache.Sheet = "xl/worksheets/sheet2.xml"
				case "size":
					cache.XMLSize++
				case "sourceCRC":
					cache.SourceCRC32++
				case "duplicateID":
					cache.Value.IDs = append(cache.Value.IDs, cache.Value.IDs[0])
				}
				cache.Checksum = cache.checksum()
				if mode == "checksum" {
					cache.Checksum = "wrong"
				}
				raw, _ = json.Marshal(cache)
				if err := os.WriteFile(cachePath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			reopened, err := OpenWorkbook(file)
			if err != nil {
				t.Fatal(err)
			}
			reloaded := reopened.GetCachedIndex(meta.Path)
			if reloaded == nil {
				t.Fatal("missing normal index")
			}
			if _, err := reopened.ConfirmCatalogue(context.Background(), meta, idx, nil); err == nil {
				t.Fatal("index from a different workbook object accepted")
			}
			if mode == "XMLCorruption" {
				raw, err := os.ReadFile(idx.XMLPath)
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(strings.Replace(string(raw), "9007199254741015.11", "9007199254741015.12", 1))
				if err := os.WriteFile(idx.XMLPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			progress := 0
			value, err := reopened.ConfirmCatalogue(context.Background(), meta, reloaded, func(int64, int64, string) { progress++ })
			if mode == "XMLCorruption" {
				if err == nil || len(value.Items) > 0 {
					t.Fatal("modified cached source values passed CRC proof", value, err)
				}
				return
			}
			if err != nil || progress < 1 {
				t.Fatal("missing proof did not run explicit bounded-memory rebuild", progress, err)
			}
			catalogueAssertConfirmed(t, value)
			progress = 0
			if _, err := reopened.ConfirmCatalogue(context.Background(), meta, reloaded, func(int64, int64, string) { progress++ }); err != nil || progress != 0 {
				t.Fatal("confirmed proof rescanned cached worksheet", progress, err)
			}
		})
	}
}

type catalogueWaitContext struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func TestCatalogueReadPreparedWarmIndexUsesSmallProofWithoutBodyAndRejectsBrokenProof(t *testing.T) {
	isolateTestStorage(t)
	_, meta, idx, file := catalogueIndexedFixture(t, catalogueSourceParts())
	valid, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	broken, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if valid.GetCachedIndex(meta.Path) == nil || broken.GetCachedIndex(meta.Path) == nil {
		t.Fatal("prepared warm index not loaded")
	}
	aside := idx.XMLPath + ".catalogue-aside"
	if err := os.Rename(idx.XMLPath, aside); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(aside, idx.XMLPath)
	value, err := valid.ReadCatalogue(context.Background())
	if err != nil {
		t.Fatal("warm metadata listing read the worksheet body", err)
	}
	catalogueAssertConfirmed(t, value)
	if err := os.WriteFile(broken.worksheetCataloguePath(meta), []byte(`{"version":1,"checksum":"broken"}`), 0600); err != nil {
		t.Fatal(err)
	}
	value, err = broken.ReadCatalogue(context.Background())
	if err != nil {
		t.Fatal("broken proof triggered an implicit body rebuild", err)
	}
	item := catalogueFind(t, value, "账表", "")
	if item.Status == "ready" || item.Table.Verified || len(item.Areas) > 0 {
		t.Fatal("broken cached proof enabled a misleading target", item)
	}
}

func (c *catalogueWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestCatalogueConfirmationCancelledOwnerDoesNotCancelWaitingConsumerOrPublishPartialProof(t *testing.T) {
	isolateTestStorage(t)
	wb, meta, _, file := catalogueIndexedFixture(t, catalogueSourceParts())
	if err := os.Remove(wb.worksheetCataloguePath(meta)); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx := reopened.GetCachedIndex(meta.Path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var owner Catalogue
	ownerDone := make(chan error, 1)
	go func() {
		var err error
		owner, err = reopened.ConfirmCatalogue(ctx, meta, idx, func(int64, int64, string) { close(entered); <-release })
		ownerDone <- err
	}()
	<-entered
	waitCtx := &catalogueWaitContext{Context: context.Background(), waiting: make(chan struct{})}
	var waiter Catalogue
	waiterDone := make(chan error, 1)
	go func() {
		var err error
		waiter, err = reopened.ConfirmCatalogue(waitCtx, meta, idx, nil)
		waiterDone <- err
	}()
	select {
	case <-waitCtx.waiting:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("second consumer did not share the in-flight confirmation")
	}
	cancel()
	close(release)
	if err := <-ownerDone; !errors.Is(err, context.Canceled) || len(owner.Items) != 0 {
		t.Fatal("cancelled owner published a result", owner, err)
	}
	select {
	case err := <-waiterDone:
		if err != nil {
			t.Fatal("active waiter inherited another request's cancellation", err)
		}
		catalogueAssertConfirmed(t, waiter)
	case <-time.After(5 * time.Second):
		t.Fatal("waiting consumer did not retry cancelled proof")
	}
	reopened.catalogueMu.Lock()
	defer reopened.catalogueMu.Unlock()
	if len(reopened.catalogueLoading) != 0 || reopened.catalogueBytes > formats.CatalogueSheetCacheBytes {
		t.Fatal("confirmation loading ownership or memory not reclaimed")
	}
}

func TestCatalogueDeclarationMemoHasIndependentEntryAndByteBudgets(t *testing.T) {
	wb := new(Workbook)
	for i := 0; i < formats.CatalogueSheetCacheEntries+1; i++ {
		wb.cacheWorksheetCatalogue(SheetMeta{Path: fmt.Sprintf("sheet-%d", i)}, worksheetCatalogueDeclarations{IDs: []string{"r1"}})
	}
	if _, found := wb.cachedWorksheetCatalogue(SheetMeta{Path: "sheet-0"}); found || len(wb.catalogueSheets) != formats.CatalogueSheetCacheEntries {
		t.Fatal("declaration LRU entry capacity not enforced")
	}
	large := worksheetCatalogueDeclarations{}
	for i := 0; i < 50; i++ {
		large.IDs = append(large.IDs, fmt.Sprintf("%02d", i)+strings.Repeat("x", formats.CatalogueDefinitionMax-2))
	}
	wb.cacheWorksheetCatalogue(SheetMeta{Path: "too-large"}, large)
	if _, found := wb.cachedWorksheetCatalogue(SheetMeta{Path: "too-large"}); found || wb.catalogueBytes > formats.CatalogueSheetCacheBytes {
		t.Fatal("oversize proof entered the bounded memo")
	}
}

func TestCatalogueProofCacheWriteFailureDoesNotFailOrdinaryIndexOrInventMissingMetadata(t *testing.T) {
	isolateTestStorage(t)
	file := writeCatalogueZIPFixture(t, catalogueSourceParts(), false)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := wb.FindSheet("原表")
	blocked := wb.worksheetCataloguePath(meta)
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(blocked, "retained")
	if err := os.WriteFile(sentinel, []byte("derived-cache-path-blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil || idx.RowCount() != 1 {
		t.Fatal("optional proof cache prevented ordinary index publication", err)
	}
	value, err := wb.ReadCatalogue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	catalogueAssertConfirmed(t, value)
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := reopened.GetCachedIndex(meta.Path)
	value, err = reopened.ConfirmCatalogue(context.Background(), meta, reloaded, nil)
	if err != nil {
		t.Fatal("unwritable derived cache blocked live confirmation", err)
	}
	catalogueAssertConfirmed(t, value)
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "derived-cache-path-blocked" {
		t.Fatal("cache write removed a different target", err)
	}
	left, err := filepath.Glob(filepath.Join(wb.CacheDir, ".source-catalogue-*"))
	if err != nil || len(left) != 0 {
		t.Fatal("failed derived write leaked temporary files", left, err)
	}
}
