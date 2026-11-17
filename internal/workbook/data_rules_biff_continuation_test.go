package workbook

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDataRulesBIFFContinuationAllFieldAndCharacterBoundaries(t *testing.T) {
	data := biffDataRuleRecord(1, []byte{0x1e, 1, 0}, []byte{0x1e, 9, 0}, CellMerge{Row: 1, Col: 1, EndRow: 65536, EndCol: 1})
	type span struct{ first, last int }
	var spans []span
	for at, n := 4, 0; n < 4; n++ {
		chars := int(xlsUint16(data, at))
		first := at + 3
		last := first + chars*2
		spans = append(spans, span{first, last})
		at = last
	}
	for split := 1; split < len(data); split++ {
		first, second := bytes.Clone(data[:split]), bytes.Clone(data[split:])
		bad := false
		for _, s := range spans {
			if split >= s.first && split < s.last {
				if (split-s.first)%2 != 0 {
					bad = true
				}
				second = append([]byte{1}, second...)
				break
			}
		}
		rule, err := readBIFFDataRuleParts(context.Background(), [][]byte{first, second})
		if bad {
			if err == nil {
				t.Fatal("UTF-16 unit split in the middle of a byte accepted", split)
			}
			continue
		}
		if err != nil || rule.PromptTitle != "源提示" || rule.Formula1 != "=1" || rule.Formula2 != "=9" || rule.SqRef != "A1:A65536" || rule.RecordHex != hex.EncodeToString(first) || !reflect.DeepEqual(rule.ContinuationHex, []string{hex.EncodeToString(second)}) {
			t.Fatal(split, rule, err)
		}
	}
}

func TestDataRulesBIFFContinuationUnicodeWidthAndSurrogateRealCFB(t *testing.T) {
	isolateTestStorage(t)
	ordinary := biffDataRuleRecord(0, nil, nil, CellMerge{Row: 1, Col: 1, EndRow: 65536, EndCol: 1})
	firstTitleSize := 3 + int(xlsUint16(ordinary, 4))*2
	first := append(bytes.Clone(ordinary[:4]), 4, 0, 0, 'A')
	second := []byte{1, 0x2D, 0x4E, 0x3D, 0xD8}
	third := append([]byte{1, 0, 0xDE}, ordinary[4+firstTitleSize:]...)
	file := filepath.Join(t.TempDir(), "continued-rule.xls")
	writeBIFF8Fixture(t, file, biffDataRuleHeader(1), xlsRecord(biffDv, first), xlsRecord(0x003C, second), xlsRecord(0x003C, third))
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
	page, err := index.At(context.Background(), 65536, 1, 0, 50)
	if err != nil || page.Total != 1 || !page.ScopeComplete || page.Items[0].PromptTitle != "A中😀" || len(page.Items[0].ContinuationHex) != 2 {
		t.Fatal(page, err)
	}
	page.Items[0].ContinuationHex[0] = "changed"
	if allDataRules(t, index)[0].ContinuationHex[0] != hex.EncodeToString(second) {
		t.Fatal("public continuation bytes mutated cached source")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("continued rules changed source")
	}
	bad := bytes.Clone(third)
	bad[1], bad[2] = 0, 0
	if _, err := readBIFFDataRuleParts(context.Background(), [][]byte{first, second, bad}); err == nil {
		t.Fatal("unpaired surrogate became replacement text")
	}
	if _, err := readBIFFDataRuleParts(context.Background(), [][]byte{first, second}); err == nil {
		t.Fatal("truncated rule became a partial valid rule")
	}
}

func TestDataRulesBIFFActualLargeRulePreservesLogicalFormulaAndPhysicalRecords(t *testing.T) {
	isolateTestStorage(t)
	tokens := []byte{0x1e, 1, 0}
	for range 2500 {
		tokens = append(tokens, 0x1e, 1, 0, 0x03)
	}
	data := biffDataRuleRecord(7, tokens, nil, CellMerge{Row: 1, Col: 1, EndRow: 65536, EndCol: 1})
	if len(data) <= 8224 {
		t.Fatal("fixture is not larger than an actual BIFF record")
	}
	first, second := data[:8224], data[8224:]
	file := filepath.Join(t.TempDir(), "large-rule.xls")
	writeBIFF8Fixture(t, file, biffDataRuleHeader(1), xlsRecord(biffDv, first), xlsRecord(0x003C, second))
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
	rule := allDataRules(t, index)[0]
	if rule.Formula1Tokens != hex.EncodeToString(tokens) || rule.RecordHex != hex.EncodeToString(first) || !reflect.DeepEqual(rule.ContinuationHex, []string{hex.EncodeToString(second)}) || rule.Status != "unresolved" || rule.Formula1 != "" {
		t.Fatal("large expression lost source bytes or exceeded render work without an explicit state", rule)
	}
}

func TestDataRulesBIFFExpressionUsesCoordinatesAndPreservesOriginDependentTokens(t *testing.T) {
	for _, item := range []struct {
		tokens          []byte
		formula, status string
	}{
		{[]byte{0x44, 4, 0, 1, 0xC0, 0x1e, 2, 0, 0x05, 0x41, 0x14, 0}, "=SQRT((B5*2))", "described"},
		{[]byte{0x41, 34, 0}, "=TRUE()", "described"},
		{[]byte{0x4C, 0, 0, 0, 0xC0}, "", "unresolved"},
		{[]byte{0x23, 1, 0, 0, 0}, "", "unresolved"},
	} {
		data := biffDataRuleRecord(7, item.tokens, nil, CellMerge{Row: 2, Col: 1, EndRow: 65536, EndCol: 1})
		rule, err := readBIFFDataRule(data)
		if err != nil || rule.Formula1 != item.formula || rule.Status != item.status || rule.Formula1Tokens != hex.EncodeToString(item.tokens) {
			t.Fatal(item, rule, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readBIFFDataRuleParts(ctx, [][]byte{make([]byte, 4)}); err == nil {
		t.Fatal("cancelled decoding succeeded")
	}
	if _, err := readBIFFDataRuleParts(context.Background(), [][]byte{bytes.Repeat([]byte{0}, 65537)}); err == nil {
		t.Fatal("oversize metadata accepted")
	}
}

func FuzzDataRuleBIFFContinuations(f *testing.F) {
	data := biffDataRuleRecord(1, []byte{0x1e, 1, 0}, []byte{0x1e, 9, 0}, CellMerge{Row: 1, Col: 1, EndRow: 65536, EndCol: 1})
	packed := binary.LittleEndian.AppendUint16(nil, uint16(len(data)))
	packed = append(packed, data...)
	f.Add(packed)
	f.Add([]byte{0, 0, 4, 0, 1, 0, 0, 0})
	f.Fuzz(func(t *testing.T, packed []byte) {
		if len(packed) > 128<<10 {
			t.Skip()
		}
		var parts [][]byte
		for at := 0; at < len(packed); {
			if at+2 > len(packed) {
				return
			}
			n := int(xlsUint16(packed, at))
			at += 2
			if n > len(packed)-at {
				return
			}
			parts = append(parts, packed[at:at+n])
			at += n
			if len(parts) > 256 {
				return
			}
		}
		before := bytes.Clone(packed)
		rule, err := readBIFFDataRuleParts(context.Background(), parts)
		if err != nil {
			return
		}
		if !bytes.Equal(before, packed) || len(parts) == 0 || rule.RecordHex != hex.EncodeToString(parts[0]) || len(rule.ContinuationHex) != len(parts)-1 {
			t.Fatal("continued source record evidence changed")
		}
		for at, part := range parts[1:] {
			if rule.ContinuationHex[at] != hex.EncodeToString(part) {
				t.Fatal("continuation content changed")
			}
		}
	})
}
