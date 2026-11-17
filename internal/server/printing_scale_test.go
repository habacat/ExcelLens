package server

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"excellens/internal/buildinfo"
	"excellens/internal/printing"
	"excellens/internal/workbook"
)

// These are independent physical BIFF8/CFB records, not an adapter-shaped
// in-memory mock. The same generator serves the small default and full probes.
func printBIFFSource(t *testing.T, path string, rows int) {
	t.Helper()
	record := func(id uint16, data []byte) []byte {
		head := binary.LittleEndian.AppendUint16(nil, id)
		head = binary.LittleEndian.AppendUint16(head, uint16(len(data)))
		return append(head, data...)
	}
	u16 := func(value string) []byte {
		units := utf16.Encode([]rune(value))
		out := binary.LittleEndian.AppendUint16(nil, uint16(len(units)))
		out = append(out, 1)
		for _, unit := range units {
			out = binary.LittleEndian.AppendUint16(out, unit)
		}
		return out
	}
	stream := record(0x0809, []byte{0, 6, 5, 0})
	bound := append(make([]byte, 6), byte(2), byte(1))
	bound = append(bound, u16("明细")[3:]...)
	position := len(stream) + 4
	stream = append(stream, record(0x0085, bound)...)
	stream = append(stream, record(0x000a, nil)...)
	binary.LittleEndian.PutUint32(stream[position:], uint32(len(stream)))
	stream = append(stream, record(0x0809, []byte{0, 6, 0x10, 0})...)
	stream = append(stream, record(0x0225, []byte{0, 0, 44, 1})...)
	stream = append(stream, record(0x0055, []byte{8, 0})...)
	for number := 1; number <= rows; number++ {
		row := make([]byte, 16)
		binary.LittleEndian.PutUint16(row, uint16(number-1))
		binary.LittleEndian.PutUint16(row[6:], 300)
		row[13] = 1
		if number%997 == 0 {
			row[12] = 0x20
		}
		stream = append(stream, record(0x0208, row)...)
		label := make([]byte, 6)
		binary.LittleEndian.PutUint16(label, uint16(number-1))
		stream = append(stream, record(0x0204, append(label, u16(fmt.Sprintf("%010d", number))...))...)
		numeric := make([]byte, 14)
		binary.LittleEndian.PutUint16(numeric, uint16(number-1))
		binary.LittleEndian.PutUint16(numeric[2:], 1)
		binary.LittleEndian.PutUint64(numeric[6:], math.Float64bits(float64(number)+0.5))
		stream = append(stream, record(0x0203, numeric)...)
	}
	stream = append(stream, record(0x000a, nil)...)
	size := max(4096, len(stream))
	sectors, fats := (size+511)/512, 1
	for (sectors+1+fats+127)/128 > fats {
		fats = (sectors + 1 + fats + 127) / 128
	}
	if fats > 109 {
		t.Fatal("generated BIFF source exceeds CFB header DIFAT capacity")
	}
	const free, end, fatID = uint32(0xffffffff), uint32(0xfffffffe), uint32(0xfffffffd)
	out := make([]byte, 512+512*(sectors+1+fats))
	header := out[:512]
	copy(header, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	for at, value := range map[int]uint16{24: 0x3e, 26: 3, 28: 0xfffe, 30: 9, 32: 6} {
		binary.LittleEndian.PutUint16(header[at:], value)
	}
	for at, value := range map[int]uint32{44: uint32(fats), 48: uint32(sectors), 56: 4096, 60: end, 68: end} {
		binary.LittleEndian.PutUint32(header[at:], value)
	}
	for at := 0; at < 109; at++ {
		value := free
		if at < fats {
			value = uint32(sectors + 1 + at)
		}
		binary.LittleEndian.PutUint32(header[76+at*4:], value)
	}
	copy(out[512:], stream)
	directory := out[512+sectors*512:]
	entry := func(at int, name string, kind byte, first uint32, size uint64) {
		data := directory[at : at+128]
		units := utf16.Encode([]rune(name))
		for at, unit := range units {
			binary.LittleEndian.PutUint16(data[at*2:], unit)
		}
		binary.LittleEndian.PutUint16(data[64:], uint16((len(units)+1)*2))
		data[66], data[67] = kind, 1
		for _, offset := range []int{68, 72, 76} {
			binary.LittleEndian.PutUint32(data[offset:], free)
		}
		binary.LittleEndian.PutUint32(data[116:], first)
		binary.LittleEndian.PutUint64(data[120:], size)
	}
	entry(0, "Root Entry", 5, end, 0)
	binary.LittleEndian.PutUint32(directory[76:], 1)
	entry(128, "Workbook", 2, 0, uint64(size))
	fat := out[512+(sectors+1)*512:]
	for at := 0; at < fats*128; at++ {
		value := free
		switch {
		case at < sectors-1:
			value = uint32(at + 1)
		case at <= sectors:
			value = end
		case at <= sectors+fats:
			value = fatID
		}
		binary.LittleEndian.PutUint32(fat[at*4:], value)
	}
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
}

func printScaleHash(t *testing.T, file string) string {
	t.Helper()
	input, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, input); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func TestPrintHTTPBIFFPhysicalSourcePages(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "native.xls")
	printBIFFSource(t, file, 4)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	prepareDataRuleSheet(t, a)
	job := printStart(t, a, printOptions())
	printWait(t, a, job)
	page := printPageFor(t, a, job, 0)
	if job.Workbook.Format != "xls" {
		t.Fatal("physical fixture did not use native BIFF adapter")
	}
	seen := map[string]bool{}
	for _, cell := range page.Content.Cells {
		seen[cell.Ref] = true
		if cell.Ref == "A2" && (cell.Cell == nil || cell.Cell.Raw != "0000000002" || cell.Cell.Type != "inlineStr") {
			t.Fatal("native BIFF text/coordinate changed", cell)
		}
		if cell.Ref == "B2" && (cell.Cell == nil || cell.Cell.Raw != "2.5" || cell.Cell.Type != "n") {
			t.Fatal("native BIFF amount/coordinate changed", cell)
		}
	}
	if !seen["A2"] || !seen["B2"] {
		t.Fatal("native BIFF fixture cells were not actually returned", seen)
	}
}

func TestPrintHTTPActualMillionOOXMLAndFullBIFFSource(t *testing.T) {
	if os.Getenv("EXCELLENS_PRINT_API_SCALE") != "1" {
		t.Skip("optional actual million-row OOXML/full BIFF source and page check")
	}
	for _, format := range []string{"xlsx", "xls"} {
		t.Run(format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), "source."+format)
			records := 65536
			if format == "xls" {
				printBIFFSource(t, file, records)
			} else {
				records = 1_000_000
				output, err := os.Create(file)
				if err != nil {
					t.Fatal(err)
				}
				archive := zip.NewWriter(output)
				for name, value := range printServerParts() {
					if name == "xl/worksheets/sheet1.xml" {
						continue
					}
					entry, err := archive.Create(name)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := io.WriteString(entry, value); err != nil {
						t.Fatal(err)
					}
				}
				entry, err := archive.Create("xl/worksheets/sheet1.xml")
				if err != nil {
					t.Fatal(err)
				}
				writer := bufio.NewWriterSize(entry, 1<<20)
				fmt.Fprint(writer, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetFormatPr defaultRowHeight="15"/><sheetData>`)
				for row := 1; row <= records; row++ {
					hidden := ""
					if row%997 == 0 {
						hidden = ` hidden="1"`
					}
					if _, err := fmt.Fprintf(writer, `<row r="%d"%s><c r="A%d" t="inlineStr"><is><t>%010d</t></is></c><c r="B%d" t="n"><v>9007199254740993.31</v></c></row>`, row, hidden, row, row, row); err != nil {
						t.Fatal(err)
					}
				}
				fmt.Fprint(writer, `</sheetData><pageMargins left="0" right="0" top="0" bottom="0" header="0" footer="0"/></worksheet>`)
				if err := writer.Flush(); err != nil {
					t.Fatal(err)
				}
				if err := archive.Close(); err != nil {
					t.Fatal(err)
				}
				if err := output.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := printScaleHash(t, file)
			a, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if err := a.OpenPath(file); err != nil {
				t.Fatal(err)
			}
			prepareDataRuleSheet(t, a)
			options := printOptions()
			options.ScalePercent, options.RepeatRows = "", []printing.Interval{}
			options.Fit = &printing.Fit{Width: 1, Height: 1000}
			job := printStart(t, a, options)
			select {
			case <-job.done:
			case <-time.After(3 * time.Minute):
				t.Fatal("actual source print task did not finish within probe timeout")
			}
			printWait(t, a, job)
			if job.Index.RowCount() != records {
				t.Fatal("fixture source was not actually indexed", job.Index.RowCount(), records)
			}
			numberText := func(row int) string {
				if format == "xls" {
					return strconv.Itoa(row) + ".5"
				}
				return "9007199254740993.31"
			}
			verified := 0
			err = job.Workbook.VisitRowsContext(context.Background(), job.Index, func(yield func(int) bool) {
				for at := 0; at < records; at++ {
					if !yield(at) {
						return
					}
				}
			}, 1, 2, func(row workbook.RowData) error {
				verified++
				if row.Number != verified || len(row.Cells) != 2 || row.Cells[0].Raw != fmt.Sprintf("%010d", verified) || row.Cells[0].Type != "inlineStr" || row.Cells[1].Raw != numberText(verified) || row.Cells[1].Type != "n" {
					return fmt.Errorf("independent source row mismatch %d: %+v", verified, row)
				}
				return nil
			})
			if err != nil || verified != records {
				t.Fatal("full source/type verification failed", verified, err)
			}
			pages, cellsChecked := job.Snapshot.plan.Summary().Pages, 0
			for _, index := range []int{0, pages / 2, pages - 1} {
				page := printPageFor(t, a, job, index)
				for _, cell := range page.Content.Cells {
					if cell.Row%997 == 0 || cell.Cell == nil || cell.Cell.Ref != cell.Ref {
						t.Fatal("page lost actual source row or exposed hidden row", index, cell)
					}
					want, kind := fmt.Sprintf("%010d", cell.Row), "inlineStr"
					if cell.Col == 2 {
						want, kind = numberText(cell.Row), "n"
					}
					if cell.Cell.Raw != want || cell.Cell.Type != kind {
						t.Fatal("source page value/type mismatch", index, cell)
					}
					cellsChecked++
				}
			}
			if printScaleHash(t, file) != before {
				t.Fatal("preview changed source bytes")
			}
			report := map[string]any{"version": buildinfo.Version, "result": "PASS", "format": format, "generatedSourceRecords": records, "verifiedSourceRecords": verified, "pages": pages, "actualPagesChecked": 3, "actualPageCellsChecked": cellsChecked, "retentionBudgetedBytes": job.Snapshot.BudgetBytes(), "sourceSHA256": before, "sourceUnchanged": true, "desktopManualAcceptance": false, "officeWpsAcceptance": false, "performanceComparison": false}
			encoded, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if path := os.Getenv("EXCELLENS_PRINT_API_REPORT"); path != "" {
				path = strings.TrimSuffix(path, filepath.Ext(path)) + "-" + format + ".json"
				if err := os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Log(string(encoded))
		})
	}
}
