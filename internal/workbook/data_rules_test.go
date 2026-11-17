package workbook

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"excellens/internal/formats"
)

const dataRuleTestValues = `<sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>001</t></is></c><c r="B1"><v>9007199254740993.31</v></c><c r="C1"><f>SUM(A1:B1)</f></c></row></sheetData>`

func dataRuleFixture(t *testing.T, rules string) (*Workbook, SheetMeta, *SheetIndex, string) {
	t.Helper()
	isolateTestStorage(t)
	file := sourceMetadataWorkbook(t, `<worksheet xmlns="`+catalogueTestNS+`" xmlns:e="urn:extension">`+dataRuleTestValues+rules+`</worksheet>`, "")
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := w.Sheets[0]
	if index, err := w.SourceDataRules(context.Background(), meta, nil, nil); err == nil || index != nil || len(w.dataRuleCache) != 0 {
		t.Fatal("rule reader prepared its own index", index, err)
	}
	idx, err := w.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.dataRuleCache) != 0 || len(w.dataRuleLoading) != 0 {
		t.Fatal("ordinary indexing loaded data rules")
	}
	return w, meta, idx, file
}

func allDataRules(t *testing.T, index *SourceDataRuleIndex) []SourceDataRule {
	t.Helper()
	var result []SourceDataRule
	for offset := 0; offset < index.Summary().Rules; offset += formats.DataRulePageRows {
		page, err := index.Page(offset, formats.DataRulePageRows)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, page.Items...)
	}
	return result
}

func TestDataRulesOOXMLRealAdapterExactDefinitionsRangesAndImmutableWarmPages(t *testing.T) {
	whole := `<dataValidation type="whole" operator="between" sqref="$A$2:$A$1048576 C2:C9 A2:A3" allowBlank="true" showInputMessage="1" showErrorMessage="0" promptTitle="源标题" prompt="检查 &amp; 原文"><formula1>9007199254740993.31</formula1><formula2>9007199254741015.11</formula2></dataValidation>`
	list := `<dataValidation type="list" operator="ignored" sqref="B2:B1048576" showDropDown="1" errorStyle="warning"><formula1>"001,二,""引号"",,末尾 "</formula1></dataValidation>`
	custom := `<dataValidation type="custom" sqref="A2:D9"><formula1>LEN(D2)&gt;0</formula1></dataValidation>`
	reference := `<dataValidation type="list" sqref="D2:D9"><formula1>'其他表'!$A$1:$A$3</formula1></dataValidation>`
	w, meta, idx, file := dataRuleFixture(t, `<dataValidations count="4" disablePrompts="true">`+whole+list+custom+reference+`</dataValidations>`)
	before, _ := os.ReadFile(file)
	beforeRow, err := w.ReadRow(idx, 0, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	index, err := w.SourceDataRules(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	summary := index.Summary()
	if summary.Rules != 4 || summary.Ranges != 6 || summary.Unresolved != 1 || !summary.Complete || !summary.DisablePrompts || len(index.nodes) != 6 {
		t.Fatal(summary, len(index.nodes))
	}
	items := allDataRules(t, index)
	if items[0].Formula1 != "9007199254740993.31" || items[0].Formula2 != "9007199254741015.11" || items[0].RawXML != whole || items[0].SqRef != "$A$2:$A$1048576 C2:C9 A2:A3" || items[0].Prompt != "检查 & 原文" || !items[0].AllowBlank || !items[0].ShowInputMessage || items[0].ShowErrorMessage {
		t.Fatal(items[0])
	}
	if !reflect.DeepEqual(items[1].InlineList, []string{"001", "二", `"引号"`, "", "末尾 "}) || !items[1].SuppressDropDown || items[1].Status != "described" || items[1].ErrorStyle != "warning" {
		t.Fatal(items[1])
	}
	if items[2].Formula1 != "LEN(D2)>0" || items[2].Status != "described" || items[3].ListSource != "expression" || items[3].Status != "unresolved" || len(items[3].InlineList) != 0 {
		t.Fatal(items[2:])
	}
	for _, query := range []struct {
		row, col int
		ids      []int
	}{{2, 1, []int{1, 3}}, {3, 3, []int{1, 3}}, {1048576, 2, []int{2}}, {1, 1, nil}, {1048576, 16384, nil}} {
		page, err := index.At(context.Background(), query.row, query.col, 0, 50)
		if err != nil || page.Total != len(query.ids) {
			t.Fatal(query, page, err)
		}
		for at, id := range query.ids {
			if page.Items[at].ID != id {
				t.Fatal(page)
			}
		}
	}
	items[0].Ranges[0].EndRow = 2
	items[0].Attributes["type"] = "changed"
	items[1].InlineList[0] = "changed"
	if err := os.Rename(idx.XMLPath, idx.XMLPath+".held"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(idx.XMLPath+".held", idx.XMLPath) })
	warm, err := w.SourceDataRules(context.Background(), meta, idx, func(int64, int64, string) { t.Error("warm metadata scanned source XML") })
	if err != nil || warm != index {
		t.Fatal(warm, err)
	}
	again := allDataRules(t, warm)
	if again[0].Ranges[0].EndRow != formats.ExcelRows || again[0].Attributes["type"] != "whole" || again[1].InlineList[0] != "001" {
		t.Fatal("public result mutated cached source", again)
	}
	if err := os.Rename(idx.XMLPath+".held", idx.XMLPath); err != nil {
		t.Fatal(err)
	}
	afterRow, err := w.ReadRow(idx, 0, 1, 3)
	if err != nil || !reflect.DeepEqual(beforeRow, afterRow) || afterRow.Cells[0].Raw != "001" || afterRow.Cells[1].Raw != "9007199254740993.31" || !afterRow.Cells[2].CacheMissing {
		t.Fatal(afterRow, err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("read-only data rules changed source")
	}
	for _, bounds := range [][2]int{{-1, 1}, {0, 0}, {0, 51}, {5, 1}} {
		if _, err := index.Page(bounds[0], bounds[1]); err == nil {
			t.Fatal("bad page accepted", bounds)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := index.At(cancelled, 2, 1, 0, 50); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func scanDataRuleFixture(text string) (*SourceDataRuleIndex, error) {
	source, _ := formats.Lookup("xlsx")
	b := newDataRuleBuilder(source)
	err := scanDataRuleXML(context.Background(), strings.NewReader(text), int64(len(text)), b, nil)
	if err != nil {
		return nil, err
	}
	return b.finish(), nil
}

func TestDataRulesXMLStructureNamespacesUnknownRulesAndInvalidDefinitionsStayExplicit(t *testing.T) {
	valid := `<dataValidation type="whole" sqref="A2:A1048576"><formula1>1</formula1><formula2>9</formula2></dataValidation>`
	wrap := func(body string) string {
		return `<worksheet xmlns="` + catalogueTestNS + `" xmlns:e="urn:extension"><sheetData/>` + body + `</worksheet>`
	}
	for name, text := range map[string]string{
		"count mismatch":       wrap(`<dataValidations count="2">` + valid + `</dataValidations>`),
		"negative count":       wrap(`<dataValidations count="-1"/>`),
		"duplicate attributes": wrap(`<dataValidations><dataValidation sqref="A1" type="none" type="list"/></dataValidations>`),
		"duplicate containers": wrap(`<dataValidations/>` + `<dataValidations/>`),
		"mismatched close":     strings.Replace(wrap(`<dataValidations>`+valid+`</dataValidations>`), `</formula1>`, `</formula2>`, 1),
		"trailing root":        wrap("") + `<worksheet/>`,
		"trailing text":        wrap("") + `x`,
		"closing attributes":   strings.Replace(wrap(""), `</worksheet>`, `</worksheet bad="1">`, 1),
		"truncated":            strings.TrimSuffix(wrap(""), `</worksheet>`),
		"before data":          `<worksheet><dataValidations/><sheetData/></worksheet>`,
		"doctype":              `<!DOCTYPE worksheet><worksheet><sheetData/></worksheet>`,
		"oversize block":       wrap(`<dataValidations><!--` + strings.Repeat(" ", formats.DataRuleBlockBytes) + `--></dataValidations>`),
		"oversize declaration": wrap(fmt.Sprintf(`<dataValidations count="%d"/>`, formats.DataRuleCount+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if index, err := scanDataRuleFixture(text); err == nil || index != nil {
				t.Fatal("bad XML became a successful partial rules list", index, err)
			}
		})
	}
	for name, body := range map[string]string{
		"foreign direct": `<e:dataValidations><e:dataValidation type="list" sqref="A1"/></e:dataValidations>`,
		"x14 extension":  `<extLst><ext uri="data"><e:dataValidations count="1"><e:dataValidation/></e:dataValidations></ext></extLst>`,
	} {
		t.Run(name, func(t *testing.T) {
			index, err := scanDataRuleFixture(wrap(body))
			if err != nil || index.Summary().Complete || len(index.Summary().Notices) == 0 {
				t.Fatal(index, err)
			}
		})
	}
	prefixed := `<s:worksheet xmlns:s="` + catalogueTestNS + `"><s:sheetData/><s:dataValidations count="1">` + strings.NewReplacer("dataValidation", "s:dataValidation", "formula1", "s:formula1", "formula2", "s:formula2").Replace(valid) + `</s:dataValidations></s:worksheet>`
	if index, err := scanDataRuleFixture(prefixed); err != nil || index.Summary().Rules != 1 {
		t.Fatal(index, err)
	}
	invalid := []string{
		`<dataValidation type="whole" sqref="A2$" allowBlank="maybe"><formula1>1</formula1><formula2>9</formula2></dataValidation>`,
		`<dataValidation type="whole" sqref="A2"><formula1>1<e:x>9</e:x>2</formula1><formula2>9</formula2></dataValidation>`,
		`<dataValidation type="whole" sqref="A2"><formula1>1</formula1><formula1>2</formula1><formula2>9</formula2></dataValidation>`,
	}
	index, err := scanDataRuleFixture(wrap(`<dataValidations count="3">` + strings.Join(invalid, "") + `</dataValidations>`))
	if err != nil || index.Summary().Unresolved != 3 {
		t.Fatal(index, err)
	}
	items := allDataRules(t, index)
	if items[0].Attributes["allowBlank"] != "maybe" || items[0].RawXML != invalid[0] || len(items[0].Ranges) != 0 || items[1].Formula1 != "" || items[2].Formula1 != "" {
		t.Fatal("invalid source formula fabricated a valid expression", items)
	}
	empty, err := index.At(context.Background(), 1, 1, 0, 50)
	if err != nil || empty.Total != 0 || empty.ScopeComplete || index.Summary().UnknownScopes != 1 {
		t.Fatal("unknown source scope became proof that the cell has no rule", empty, err)
	}
}

func TestDataRulesSourceIdentityCorruptXMLCancellationAndNoImplicitOtherSheet(t *testing.T) {
	w, meta, idx, file := dataRuleFixture(t, `<dataValidations><dataValidation type="list" sqref="A2"><formula1>"001,002"</formula1></dataValidation></dataValidations>`)
	bad := *idx
	if _, err := w.SourceDataRules(context.Background(), meta, &bad, nil); err == nil {
		t.Fatal("foreign index accepted")
	}
	wrong := meta
	wrong.Name += "wrong"
	if _, err := w.SourceDataRules(context.Background(), wrong, idx, nil); err == nil {
		t.Fatal("wrong source sheet accepted")
	}
	original, _ := os.ReadFile(idx.XMLPath)
	if err := os.WriteFile(idx.XMLPath, bytes.Replace(original, []byte("001"), []byte("009"), 1), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := w.SourceDataRules(context.Background(), meta, idx, nil); err == nil || value != nil || len(w.dataRuleCache) != 0 {
		t.Fatal("bad XML cached rules", value, err)
	}
	if err := os.WriteFile(idx.XMLPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if value, err := w.SourceDataRules(ctx, meta, idx, func(int64, int64, string) { cancel() }); !errors.Is(err, context.Canceled) || value != nil || len(w.dataRuleCache) != 0 {
		t.Fatal("cancelled read published metadata", value, err)
	}
	if value, err := w.SourceDataRules(context.Background(), meta, idx, nil); err != nil || value.Summary().Rules != 1 {
		t.Fatal(value, err)
	}
	source, _ := os.ReadFile(file)
	if err := os.WriteFile(file, append(source, byte(0)), 0600); err != nil {
		t.Fatal(err)
	}
	// ZIP fingerprints intentionally cover archive members rather than trailing
	// packaging bytes. Replace a source member through a freshly generated file.
	changed := sourceMetadataWorkbook(t, `<worksheet><sheetData/><dataValidations/></worksheet>`, "")
	updated, _ := os.ReadFile(changed)
	if err := os.WriteFile(file, updated, 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := w.SourceDataRules(context.Background(), meta, idx, nil); err == nil || value != nil {
		t.Fatal("warm cache hid source changes", value, err)
	}
}

type observedDataRuleContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (ctx *observedDataRuleContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.observed) })
	return ctx.Context.Done()
}

func TestDataRulesSharedLoadingCancellationRetryAndBoundedImmutableLRU(t *testing.T) {
	w, meta, idx, _ := dataRuleFixture(t, `<dataValidations><dataValidation type="none" sqref="A1:A1048576"/></dataValidations>`)
	entered, release := make(chan struct{}), make(chan struct{})
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerDone := make(chan error, 1)
	go func() {
		_, err := w.SourceDataRules(ownerCtx, meta, idx, func(int64, int64, string) { close(entered); <-release })
		ownerDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not enter metadata read")
	}
	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitObserved := &observedDataRuleContext{Context: waitCtx, observed: make(chan struct{})}
	waitDone := make(chan error, 1)
	go func() { _, err := w.SourceDataRules(waitObserved, meta, idx, nil); waitDone <- err }()
	select {
	case <-waitObserved.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not reach the shared metadata load")
	}
	cancelWait()
	if err := <-waitDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	retryObserved := &observedDataRuleContext{Context: context.Background(), observed: make(chan struct{})}
	var attempts atomic.Int32
	var group sync.WaitGroup
	results := make(chan *SourceDataRuleIndex, 8)
	errorsFound := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := w.SourceDataRules(retryObserved, meta, idx, func(int64, int64, string) { attempts.Add(1) })
			if err != nil {
				errorsFound <- err
			}
			results <- value
		}()
	}
	select {
	case <-retryObserved.observed:
	case <-time.After(5 * time.Second):
		t.Fatal("retry reader did not wait for the owner")
	}
	cancelOwner()
	close(release)
	if err := <-ownerDone; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal("waiter inherited another caller's cancellation", err)
	}
	var first *SourceDataRuleIndex
	for result := range results {
		if first == nil {
			first = result
		}
		if result != first || result.Summary().Rules != 1 {
			t.Fatal("shared read did not publish one immutable index", result)
		}
	}
	if attempts.Load() != 1 {
		t.Fatal("duplicate source scan", attempts.Load())
	}
	w.dataRuleMu.Lock()
	for at := 0; at < formats.DataRuleCacheSheets+3; at++ {
		w.cacheDataRulesLocked(fmt.Sprint(at), first)
	}
	if len(w.dataRuleCache) > formats.DataRuleCacheSheets || w.dataRuleBytes > formats.DataRuleCacheBytes {
		t.Fatal("unbounded rule cache")
	}
	tooLarge := *first
	tooLarge.bytes = formats.DataRuleCacheBytes + 1
	w.cacheDataRulesLocked("oversize", &tooLarge)
	_, found := w.dataRuleCache["oversize"]
	w.dataRuleMu.Unlock()
	if found {
		t.Fatal("oversize index retained")
	}
	if page, err := first.At(context.Background(), 1048576, 1, 0, 50); err != nil || page.Total != 1 {
		t.Fatal("LRU eviction broke a reader snapshot", page, err)
	}
}

func biffDataRuleHeader(count int) []byte {
	data := make([]byte, 18)
	binary.LittleEndian.PutUint32(data[10:], ^uint32(0))
	binary.LittleEndian.PutUint32(data[14:], uint32(count))
	return xlsRecord(biffDVal, data)
}

func biffDataRuleRecord(flags uint32, first, second []byte, areas ...CellMerge) []byte {
	data := binary.LittleEndian.AppendUint32(nil, flags)
	for _, text := range []string{"源提示", "错误标题", "输入说明", "请核对"} {
		data = append(data, xlsU16(text)...)
	}
	for _, tokens := range [][]byte{first, second} {
		data = binary.LittleEndian.AppendUint16(data, uint16(len(tokens)))
		data = append(data, 0, 0)
		data = append(data, tokens...)
	}
	data = binary.LittleEndian.AppendUint16(data, uint16(len(areas)))
	for _, area := range areas {
		for _, value := range []int{area.Row - 1, area.EndRow - 1, area.Col - 1, area.EndCol - 1} {
			data = binary.LittleEndian.AppendUint16(data, uint16(value))
		}
	}
	return data
}

func biffDataRuleStringToken(text string) []byte {
	data := xlsU16(text)
	return append([]byte{0x17, byte(len(utf16.Encode([]rune(text))))}, data[2:]...)
}

func TestDataRulesBIFFRealCFBRecordsInlineListBoundariesEmbeddedChartAndSourceEvidence(t *testing.T) {
	isolateTestStorage(t)
	area := CellMerge{Row: 2, Col: 1, EndRow: 65536, EndCol: 1}
	listTokens := biffDataRuleStringToken("001\x00第二项\x00\x00末尾 ")
	list := biffDataRuleRecord(3|0x80|0x100|0x200|0x40000|0x80000|1<<4|15<<20, listTokens, nil, area, area)
	whole := biffDataRuleRecord(1, []byte{0x1e, 1, 0}, []byte{0x1e, 9, 0}, CellMerge{Row: 2, Col: 1, EndRow: 9, EndCol: 2})
	file := filepath.Join(t.TempDir(), "rules.xls")
	writeBIFF8Fixture(t, file, xlsRecord(0x0809, []byte{0, 6, 0x20, 0}), biffDataRuleHeader(99), xlsRecord(biffDv, []byte{1}), xlsRecord(0x000A, nil), biffDataRuleHeader(2), xlsRecord(biffDv, list), xlsRecord(biffDv, whole), xlsRecord(0x00EC, []byte{0}), xlsRecord(0x003C, []byte{0}))
	before, _ := os.ReadFile(file)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := w.Sheets[0]
	idx, err := w.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	index, err := w.SourceDataRules(context.Background(), meta, idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := allDataRules(t, index)
	if index.Summary().Rules != 2 || len(index.nodes) != 3 || !index.Summary().Complete || index.Summary().Unresolved != 0 {
		t.Fatal(index.Summary())
	}
	if !reflect.DeepEqual(items[0].InlineList, []string{"001", "第二项", "", "末尾 "}) || !items[0].SuppressDropDown || !items[0].AllowBlank || !items[0].ShowInputMessage || !items[0].ShowErrorMessage || items[0].RecordHex != hex.EncodeToString(list) || items[0].Formula1Tokens != hex.EncodeToString(listTokens) || items[0].Formula1 != "" {
		t.Fatal(items[0])
	}
	if items[1].Formula1 != "=1" || items[1].Formula2 != "=9" || items[1].PromptTitle != "源提示" {
		t.Fatal(items[1])
	}
	page, err := index.At(context.Background(), 2, 1, 0, 50)
	if err != nil || page.Total != 2 {
		t.Fatal(page, err)
	}
	page, err = index.At(context.Background(), 65536, 1, 0, 50)
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
	row, err := w.ReadRow(idx, 1, 1, 2)
	if err != nil || row.Cells[0].Raw != "000000123456789" || row.Cells[0].Type != "inlineStr" {
		t.Fatal(row, err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("BIFF rules changed source")
	}
}

func TestDataRulesSourceRangeTreeMatchesIndependentMembershipOracle(t *testing.T) {
	random := rand.New(rand.NewSource(20151115))
	source, _ := formats.Lookup("xlsx")
	b := newDataRuleBuilder(source)
	for id := 0; id < 200; id++ {
		rule := SourceDataRule{Type: "none", Status: "described"}
		for range 1 + random.Intn(8) {
			row, col := 1+random.Intn(80), 1+random.Intn(24)
			rule.Ranges = append(rule.Ranges, CellMerge{Row: row, Col: col, EndRow: row + random.Intn(25), EndCol: col + random.Intn(7)})
		}
		if err := b.add(rule); err != nil {
			t.Fatal(err)
		}
	}
	index := b.finish()
	for row := 1; row <= 110; row++ {
		for col := 1; col <= 32; col++ {
			var expected []int
			for _, rule := range index.rules {
				for _, area := range rule.Ranges {
					if area.Row <= row && row <= area.EndRow && area.Col <= col && col <= area.EndCol {
						expected = append(expected, rule.ID)
						break
					}
				}
			}
			var got []int
			for offset := 0; ; offset += formats.DataRulePageRows {
				page, err := index.At(context.Background(), row, col, offset, formats.DataRulePageRows)
				if err != nil {
					t.Fatal(err)
				}
				for _, rule := range page.Items {
					got = append(got, rule.ID)
				}
				if offset+len(page.Items) >= page.Total {
					break
				}
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatal("range index differs from source-area membership", row, col, got, expected)
			}
		}
	}
}

func TestDataRulesBoundsUnknownTypeAndDefaultsPreserveSourceDefinitions(t *testing.T) {
	for _, raw := range []string{"A1:A1048577", "XFE1", "A0", "A01", "A+1", "A1$", "A$$1", "$A1:$B0", "A2:A1", "A1:B2:C3", "A1:", "A1 A2$"} {
		if areas, err := dataRuleRanges(raw, formats.ExcelRows, formats.ExcelColumns); err == nil || areas != nil {
			t.Fatal("invalid source boundaries partially adopted", raw, areas, err)
		}
	}
	if areas, err := dataRuleRanges("$A$1:$XFD$1048576\tB2\nC3", formats.ExcelRows, formats.ExcelColumns); err != nil || len(areas) != 3 {
		t.Fatal(areas, err)
	}
	if areas, err := dataRuleRanges("A65537", formats.BIFFRows, formats.BIFFColumns); err == nil || areas != nil {
		t.Fatal("BIFF source ceiling lost", areas, err)
	}
	text := `<worksheet><sheetData/><dataValidations count="2"><dataValidation sqref="A1"/><dataValidation type="unknown" sqref="A2" e="original"><formula1>0</formula1></dataValidation></dataValidations></worksheet>`
	index, err := scanDataRuleFixture(text)
	if err != nil {
		t.Fatal(err)
	}
	items := allDataRules(t, index)
	if items[0].Type != "none" || items[0].ErrorStyle != "stop" || items[0].AllowBlank || items[0].ShowErrorMessage || items[0].SuppressDropDown || items[0].Status != "described" || len(items[0].Attributes) != 1 {
		t.Fatal("schema defaults lost the distinction from saved attributes", items[0])
	}
	if items[1].Type != "unknown" || items[1].Status != "invalid" || items[1].Attributes["e"] != "original" || items[1].RawXML == "" {
		t.Fatal(items[1])
	}
}

func TestDataRulesBIFFMalformedLengthsDeclarationsAndUnrestorableFormulas(t *testing.T) {
	data := biffDataRuleRecord(1, []byte{0x4C, 0, 0, 0, 0xC0}, []byte{0x1e, 9, 0}, CellMerge{Row: 2, Col: 1, EndRow: 65536, EndCol: 1})
	rule, err := readBIFFDataRule(data)
	if err != nil || rule.Status != "unresolved" || rule.Formula1 != "" || rule.Formula1Tokens == "" || rule.RecordHex != hex.EncodeToString(data) {
		t.Fatal(rule, err)
	}
	for size := 0; size < len(data); size++ {
		if _, err := readBIFFDataRule(data[:size]); err == nil {
			t.Fatal("truncated Dv accepted", size)
		}
	}
	for _, records := range [][][]byte{
		{xlsRecord(biffDv, data)},
		{biffDataRuleHeader(2), xlsRecord(biffDv, data)},
		{biffDataRuleHeader(0), xlsRecord(biffDv, data)},
		{biffDataRuleHeader(1), xlsRecord(biffDv, data), biffDataRuleHeader(0)},
		{biffDataRuleHeader(1), xlsRecord(biffDv, data), xlsRecord(0x003C, []byte{1})},
	} {
		file := filepath.Join(t.TempDir(), "bad.xls")
		writeBIFF8Fixture(t, file, records...)
		w, err := OpenWorkbook(file)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if index, err := w.SourceDataRules(context.Background(), w.Sheets[0], idx, nil); err == nil || index != nil {
			t.Fatal("bad collection became successful partial rules", index, err)
		}
	}
}
