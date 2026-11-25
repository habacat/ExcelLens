package workbook

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func xlsbTestRecord(dst []byte, typ uint16, body []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(typ))
	dst = binary.AppendUvarint(dst, uint64(len(body)))
	return append(dst, body...)
}

func xlsbTestWide(dst []byte, text string) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(text)))
	for _, ch := range text {
		dst = binary.LittleEndian.AppendUint16(dst, uint16(ch))
	}
	return dst
}

func writeXLSBTestWorkbook(t *testing.T, data []byte) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "test.xlsb")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	books := xlsbTestRecord(nil, brtBeginBook, nil)
	bound := binary.LittleEndian.AppendUint32(make([]byte, 4), 1)
	bound = xlsbTestWide(xlsbTestWide(bound, "rId1"), "Ledger")
	books = xlsbTestRecord(books, brtBundleSh, bound)
	books = xlsbTestRecord(books, brtEndBook, nil)
	const relType = `http://schemas.openxmlformats.org/officeDocument/2006/relationships/`
	z := zip.NewWriter(f)
	for name, raw := range map[string][]byte{
		"_rels/.rels":                []byte(`<Relationships><Relationship Id="rId1" Type="` + relType + `officeDocument" Target="xl/workbook.bin"/></Relationships>`),
		"xl/_rels/workbook.bin.rels": []byte(`<Relationships><Relationship Id="rId1" Type="` + relType + `worksheet" Target="worksheets/sheet1.bin"/></Relationships>`),
		"xl/workbook.bin":            books,
		"xl/worksheets/sheet1.bin":   data,
	} {
		out, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := out.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(z.Close(), f.Close()); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestXLSBUnknownFormulaKeepsCacheAndTruncatedPartDoesNotPublish(t *testing.T) {
	basedir := t.TempDir()
	t.Setenv("LOCALAPPDATA", basedir)
	t.Setenv("XDG_CACHE_HOME", basedir)
	sheet := xlsbTestRecord(nil, brtBeginSheet, nil)
	sheet = xlsbTestRecord(sheet, brtBeginSheetData, nil)
	sheet = xlsbTestRecord(sheet, brtRowHdr, make([]byte, 17))
	cell := binary.LittleEndian.AppendUint64(make([]byte, 8), math.Float64bits(9.5))
	cell = binary.LittleEndian.AppendUint32(append(cell, 0, 0), 1)
	cell = binary.LittleEndian.AppendUint32(append(cell, 0xFF), 0)
	sheet = xlsbTestRecord(sheet, brtFmlaNum, cell)
	sheet = xlsbTestRecord(sheet, brtEndSheetData, nil)
	good := xlsbTestRecord(sheet, brtEndSheet, nil)
	for _, testcase := range []struct {
		name string
		data []byte
		want error
	}{{"cached value", good, nil}, {"truncated header", append(good[:len(good):len(good)], 0x80), io.ErrUnexpectedEOF}} {
		t.Run(testcase.name, func(t *testing.T) {
			w, err := OpenWorkbook(writeXLSBTestWorkbook(t, testcase.data))
			if err != nil {
				t.Fatal(err)
			}
			meta := w.Sheets[0]
			index, err := w.BuildIndex(meta, nil, nil)
			if !errors.Is(err, testcase.want) {
				t.Fatal(err)
			}
			if testcase.want != nil {
				if index != nil || w.GetCachedIndex(meta.Path) != nil {
					t.Fatal("truncated sheet published an index")
				}
				return
			}
			row, err := w.ReadRow(index, 0, 1, 1)
			if err != nil || len(row.Cells) != 1 {
				t.Fatal(row, err)
			}
			unknown := row.Cells[0]
			if !unknown.HasFormula || unknown.CacheMissing || unknown.Raw != "9.5" || !strings.Contains(unknown.Formula, "令牌 FF") || strings.HasPrefix(unknown.Formula, "=") {
				t.Fatal("unsupported formula lost its identity or cache", unknown)
			}
		})
	}
}

func TestXLSBSharedStringPagesPreserveValuesAcrossSequentialAndRandomReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strings.xlsb-sst")
	const count = 5001
	data := make([]byte, xlsbSSTHeaderBytes)
	offsets := make([]byte, 0, count*8)
	text := func(id int) string { return "共享凭证编号-" + strconv.Itoa(id) }
	for id := range count {
		raw := text(id)
		rich := []byte{0, byte(id), 0, 0, 0}
		offsets = binary.LittleEndian.AppendUint64(offsets, uint64(len(data)))
		data = binary.LittleEndian.AppendUint32(data, uint32(len(raw)))
		data = binary.LittleEndian.AppendUint32(data, uint32(len(rich)))
		data = append(data, raw...)
		data = append(data, rich...)
	}
	copy(data, xlsbSSTMagic)
	binary.LittleEndian.PutUint64(data[8:], count)
	binary.LittleEndian.PutUint64(data[16:], uint64(len(data)))
	if err := os.WriteFile(path, append(data, offsets...), 0600); err != nil {
		t.Fatal(err)
	}
	sst, err := openXLSBSSTFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer sst.close()
	read := func(id int) xlsbString {
		t.Helper()
		value, err := sst.get(uint32(id))
		if err != nil || value.text != text(id) || len(value.rich) != 5 || value.rich[1] != byte(id) {
			t.Fatalf("string %d has text %q and runs %v: %v", id, value.text, value.rich, err)
		}
		return value
	}
	first := read(0)
	tail := read(count - 1)
	for id := 1; id < count; id++ {
		read(id)
	}
	read(239)
	read(0)
	if first.text != text(0) || first.rich[1] != 0 || tail.rich[1] != (count-1)%256 {
		t.Fatal("previous values changed when read-ahead pages were replaced")
	}
	if _, err := sst.get(count); err == nil {
		t.Fatal("out-of-bounds shared string was accepted")
	}
}
