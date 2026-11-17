package workbook

import (
	"context"
	"encoding/json"
	"testing"

	"excellens/internal/formats"
)

func FuzzPrintSettingsXML(f *testing.F) {
	for _, text := range []string{`<worksheet><sheetData/></worksheet>`, `<worksheet><sheetData/><pageSetup paperSize="9"/><headerFooter><oddHeader>&amp;P</oddHeader></headerFooter></worksheet>`, `<worksheet><sheetData/><rowBreaks count="1"><brk id="99" max="16383"/></rowBreaks></worksheet>`, `<worksheet><sheetData/><mergeCells count="1"><mergeCell ref="A1:B2"/></mergeCells></worksheet>`, `<worksheet><sheetData/><mergeCells count="2"><mergeCell ref="A1:B2"/></mergeCells></worksheet>`, `<worksheet><sheetData/><mergeCells count="1"><mergeCell xmlns="urn:foreign" ref="A1:B2"/></mergeCells></worksheet>`} {
		f.Add(text)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > formats.PrintTextBytes {
			return
		}
		index, err := scanPrintFixture(text)
		if err != nil {
			if index != nil {
				t.Fatal("partial index published")
			}
			return
		}
		value := index.Snapshot()
		data, err := json.Marshal(value)
		if err != nil || len(data) > formats.PrintMetadataBytes || len(value.RowBreaks)+len(value.ColBreaks) > formats.PrintBreaks || len(value.Definitions) > formats.PrintDefinitions {
			t.Fatal("unbounded successful snapshot", err)
		}
	})
}

func FuzzPrintSettingsBIFF(f *testing.F) {
	f.Add(uint16(0x00A1), biffPrintSetupFixture(2))
	f.Add(uint16(0x0014), xlsU16("源😀&P"))
	f.Add(uint16(0x001B), []byte{1, 0, 10, 0, 0, 0, 255, 63})
	f.Fuzz(func(t *testing.T, id uint16, data []byte) {
		if len(data) > formats.PrintTextBytes {
			return
		}
		b := newPrintMetadataBuilder("Data", "xls")
		if err := readBIFFPrintRecord(context.Background(), id, [][]byte{data}, b); err != nil {
			return
		}
		index, err := b.finish()
		if err != nil {
			return
		}
		value := index.Snapshot()
		if len(value.Definitions) != 1 || len(value.RowBreaks) > formats.PrintBIFFBreaks || len(value.ColBreaks) >= formats.BIFFColumns {
			t.Fatal("unbounded successful BIFF snapshot")
		}
	})
}
