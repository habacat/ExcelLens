package workbook

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"excellens/internal/formats"
)

func FuzzDataRuleXML(f *testing.F) {
	f.Add([]byte(`<worksheet><sheetData/><dataValidations count="1"><dataValidation type="list" sqref="A1:A1048576"><formula1>"001,中文,""引号"""</formula1></dataValidation></dataValidations></worksheet>`))
	f.Add([]byte(`<s:worksheet xmlns:s="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><s:sheetData/><s:dataValidations><s:dataValidation type="none" sqref="A1"/></s:dataValidations></s:worksheet>`))
	f.Add([]byte(`<worksheet><sheetData/></worksheet>extra`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128<<10 {
			t.Skip()
		}
		before := bytes.Clone(data)
		source, _ := formats.Lookup("xlsx")
		b := newDataRuleBuilder(source)
		if err := scanDataRuleXML(context.Background(), bytes.NewReader(data), int64(len(data)), b, nil); err != nil {
			return
		}
		index := b.finish()
		if len(index.rules) > formats.DataRuleCount || len(index.nodes) > formats.DataRuleRanges || !bytes.Equal(data, before) {
			t.Fatal("source or metadata budget changed")
		}
		for _, rule := range index.rules {
			if rule.RawXML == "" || !bytes.Contains(data, []byte(rule.RawXML)) {
				t.Fatal("invented original rule definition")
			}
			for _, area := range rule.Ranges {
				if area.Row < 1 || area.EndRow > formats.ExcelRows || area.Row > area.EndRow || area.Col < 1 || area.EndCol > formats.ExcelColumns || area.Col > area.EndCol {
					t.Fatal("invalid accepted source area", area)
				}
			}
		}
	})
}

func FuzzDataRuleBIFF(f *testing.F) {
	f.Add(biffDataRuleRecord(3|0x80, biffDataRuleStringToken("001\x00中文"), nil, CellMerge{Row: 1, Col: 1, EndRow: 65536, EndCol: 1}))
	f.Add(biffDataRuleRecord(1, []byte{0x1E, 1, 0}, []byte{0x1E, 9, 0}, CellMerge{Row: 2, Col: 1, EndRow: 3, EndCol: 2}))
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8224 {
			t.Skip()
		}
		before := bytes.Clone(data)
		rule, err := readBIFFDataRule(data)
		if err != nil {
			return
		}
		if !bytes.Equal(data, before) || rule.RecordHex != hex.EncodeToString(data) || len(rule.Ranges) < 1 || len(rule.Ranges) > 432 {
			t.Fatal("source or record boundaries changed")
		}
		for _, area := range rule.Ranges {
			if area.Row < 1 || area.EndRow > formats.BIFFRows || area.Row > area.EndRow || area.Col < 1 || area.EndCol > formats.BIFFColumns || area.Col > area.EndCol {
				t.Fatal("invalid accepted BIFF area", area)
			}
		}
	})
}
