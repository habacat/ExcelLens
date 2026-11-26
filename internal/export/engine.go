// Package export provides streaming, read-only exports for indexed OOXML workbooks.
// All heavy formats are produced with bounded row batches, not a workbook-wide RAM copy.
package export

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"excellens/internal/formats"
	"excellens/internal/separated"
	"excellens/internal/workbook"
	"fmt"
	"io"
	"iter"
	"math"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Format     string `json:"format"` // xlsx, csv, tsv, jsonl, html, zipcsv
	Mode       string `json:"mode"`   // values, display, formula
	BOM        bool   `json:"bom"`
	ProtectCSV bool   `json:"protectCSV"`
	Styles     bool   `json:"styles"`
}

type Selection struct {
	Sheet    workbook.SheetMeta
	Index    *workbook.SheetIndex
	FirstRow int
	LastRow  int
	FirstCol int
	LastCol  int
	// Order borrows a retained immutable source-position sequence from the job.
	Order *workbook.RowOrder
	// RowNumbers is an explicit source-row order for a visual selection,
	// including blank frozen headers that have no physical index record.
	RowNumbers []int
	// Rows borrows a repeatable source-coordinate stream from a retained job
	// snapshot, including proven blanks and already known index positions.
	Rows     RowSequence
	Filtered bool
	// Compact writes range/filtered results from A1, rather than preserving source coordinates.
	Compact bool
}

// RowSequence is an immutable borrowed selection, never a source-sized vector.
// Its owner retains every underlying index/order/view lease until Write ends.
type RowSequence interface {
	Index() *workbook.SheetIndex
	Len() int
	Rows() iter.Seq[workbook.SourceRowReference]
}

type Progress func(done, total int64, stage string)
type Tick func(n int64, stage string)

func (s Selection) RowCount() int64 {
	if s.Rows != nil {
		return int64(s.Rows.Len())
	}
	if s.RowNumbers != nil {
		return int64(len(s.RowNumbers))
	}
	if s.Filtered {
		return int64(s.Order.Len())
	}
	if s.LastRow < s.FirstRow {
		return 0
	}
	return int64(s.LastRow - s.FirstRow + 1)
}

func ValidateOptions(opts Options) error {
	switch opts.Format {
	case "xlsx", "csv", "tsv", "jsonl", "html", "zipcsv":
	default:
		return errors.New("不支持的导出格式")
	}
	if opts.Mode != "values" && opts.Mode != "display" && opts.Mode != "formula" {
		return errors.New("无效的导出内容模式")
	}
	return nil
}

func Validate(opts Options, selections []Selection) error {
	if err := ValidateOptions(opts); err != nil {
		return err
	}
	if len(selections) == 0 || len(selections) > 256 {
		return errors.New("没有可导出的工作表")
	}
	if len(selections) > 1 && opts.Format != "xlsx" && opts.Format != "zipcsv" {
		return errors.New("多工作表请使用 XLSX 或 ZIP（分 Sheet CSV）格式")
	}
	if opts.Format == "zipcsv" && len(selections) == 0 {
		return errors.New("请选择工作表")
	}
	var cells int64
	for _, s := range selections {
		if s.Index != nil && s.Index.SourceFormat == "ods" && opts.Format == "xlsx" && opts.Mode == "formula" {
			return errors.New("OpenFormula 无法直接写为 Excel 公式，请导出缓存值或显示文字")
		}
		if s.Index == nil || s.FirstCol < 1 || s.LastCol > formats.ExcelColumns || s.LastCol < s.FirstCol || s.FirstRow < 1 || s.LastRow > workbook.MaxRowsForIndex(s.Index) || s.LastRow < s.FirstRow {
			return errors.New("导出区域超出 Excel 允许范围")
		}
		if s.Order != nil && s.Order.SourceRows() != s.Index.RowCount() {
			return errors.New("导出行序与来源索引不匹配")
		}
		if s.Rows != nil {
			if s.RowNumbers != nil || s.Order != nil || !s.Filtered || !s.Compact || s.Rows.Index() != s.Index || s.Rows.Len() < 0 || s.Rows.Len() > workbook.MaxRowsForIndex(s.Index) {
				return errors.New("导出行流的来源、数量或坐标模式无效")
			}
		}
		if s.RowNumbers != nil {
			if !s.Filtered || !s.Compact {
				return errors.New("可视选区必须从 A1 紧凑导出")
			}
			for _, row := range s.RowNumbers {
				if row < 1 || row > workbook.MaxRowsForIndex(s.Index) {
					return errors.New("导出选区行号无效")
				}
			}
		}
		// Only XLSX has the OOXML row ceiling; streaming text/ZIP can
		// preserve millions of source rows without silent truncation.
		if opts.Format == "xlsx" && s.RowCount() > formats.ExcelRows {
			return errors.New("XLSX 单个工作表最多 1,048,576 行；请改用 CSV、TSV、JSONL 或 ZIP CSV 导出完整数据")
		}
		if opts.Format == "xlsx" && !s.Compact && !s.Filtered && s.LastRow > formats.ExcelRows {
			return errors.New("XLSX 无法保留超过 1,048,576 的原始行号；请选择从 A1 紧凑导出，或改用 CSV、TSV、JSONL")
		}
		if s.RowCount() > math.MaxInt64/int64(s.LastCol-s.FirstCol+1) {
			return errors.New("导出区域过大")
		}
		selectedCells := s.RowCount() * int64(s.LastCol-s.FirstCol+1)
		if cells > math.MaxInt64-selectedCells {
			return errors.New("导出区域过大")
		}
		cells += selectedCells
		if opts.Format == "xlsx" && cells > 800_000_000 {
			return errors.New("所选范围超过8亿个单元格，请缩小范围后导出")
		}
	}
	// HTML tables with millions of TD nodes are practically unusable in browsers.
	if opts.Format == "html" && cells > 150_000 {
		return errors.New("带格式 HTML 最多导出15万个单元格，请缩小范围或改用 XLSX")
	}
	// Excel's row limit applies to compacted filtered/range sheets as well.
	return nil
}

// Write writes one complete output into dst. Its caller is responsible for an
// atomic temporary file and cancellation/cleanup on failure.
func Write(ctx context.Context, dst io.Writer, wb *workbook.Workbook, selections []Selection, opts Options, progress Progress) error {
	if err := Validate(opts, selections); err != nil {
		return err
	}
	var total int64
	for _, s := range selections {
		total += s.RowCount()
	}
	var done int64
	report := func(n int64, stage string) {
		done += n
		if progress != nil {
			progress(done, total, stage)
		}
	}
	if progress != nil {
		progress(0, total, "准备导出")
	}
	switch opts.Format {
	case "xlsx":
		return writeXLSX(ctx, dst, wb, selections, opts, report)
	case "zipcsv":
		return writeCSVArchive(ctx, dst, wb, selections, opts, report)
	case "csv", "tsv":
		return writeSeparated(ctx, dst, wb, selections[0], opts, report)
	case "jsonl":
		return writeJSONL(ctx, dst, wb, selections[0], opts, report)
	case "html":
		return writeHTML(ctx, dst, wb, selections[0], opts, report)
	}
	return errors.New("unknown format")
}

// forEachRow streams with one source descriptor. Sparse gaps remain blank
// output rows; filtered/sorted records retain their exact supplied order.
func forEachRow(ctx context.Context, wb *workbook.Workbook, s Selection, emit func(original, output int, row workbook.RowData) error, report Tick) error {
	return forEachRowWithOptions(ctx, wb, s, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true}, emit, report)
}

func forEachRowWithOptions(ctx context.Context, wb *workbook.Workbook, s Selection, options workbook.RowReadOptions, emit func(original, output int, row workbook.RowData) error, report Tick) error {
	total := s.RowCount()
	var done, pending int64
	lastReport := time.Now()
	reportProgress := func(force bool) {
		if report != nil && pending > 0 && (force || (done&4095 == 0 && time.Since(lastReport) >= 250*time.Millisecond)) {
			report(pending, fmt.Sprintf("%s · %d/%d 行", s.Sheet.Name, done, total))
			pending = 0
			lastReport = time.Now()
		}
	}
	output := func(number int, row workbook.RowData) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		position := number
		if s.Filtered {
			position = int(done) + 1
		} else if s.Compact {
			position = number - s.FirstRow + 1
		}
		if err := emit(number, position, row); err != nil {
			return err
		}
		done++
		pending++
		reportProgress(false)
		return nil
	}
	if s.Rows != nil {
		seen := 0
		var sequenceErr error
		references := func(yield func(workbook.SourceRowReference) bool) {
			for ref := range s.Rows.Rows() {
				if ctx.Err() != nil {
					return
				}
				if seen >= s.Rows.Len() {
					sequenceErr = errors.New("导出行流超出已确认数量")
					return
				}
				seen++
				if !yield(ref) {
					return
				}
			}
		}
		err := wb.VisitRowsByReferencesWithOptionsContext(ctx, s.Index, references, s.FirstCol, s.LastCol, options, func(row workbook.RowData) error { return output(row.Number, row) })
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if sequenceErr != nil {
			return sequenceErr
		}
		if seen != s.Rows.Len() || done != total {
			return errors.New("导出行流未完整读取，未生成完整结果")
		}
		reportProgress(true)
		return nil
	}
	if options.ConditionalFormats {
		numbers := func(yield func(int) bool) {
			if s.RowNumbers != nil {
				for _, number := range s.RowNumbers {
					if !yield(number) {
						return
					}
				}
				return
			}
			if s.Filtered {
				for position := range s.Order.Iter() {
					if !yield(s.Index.RowAt(position).Number) {
						return
					}
				}
				return
			}
			for number := s.FirstRow; number <= s.LastRow; number++ {
				if !yield(number) {
					return
				}
			}
		}
		err := wb.VisitRowsByNumbersWithOptionsContext(ctx, s.Index, numbers, s.FirstCol, s.LastCol, options, func(row workbook.RowData) error { return output(row.Number, row) })
		if err != nil {
			return err
		}
		reportProgress(true)
		return nil
	}
	var positions iter.Seq[int]
	if s.RowNumbers != nil {
		positions = func(yield func(int) bool) {
			for _, number := range s.RowNumbers {
				i := s.Index.FindRowOffset(number)
				if i < s.Index.RowCount() && s.Index.RowAt(i).Number == number && !yield(i) {
					return
				}
			}
		}
	} else if s.Filtered {
		positions = s.Order.Iter()
	} else {
		lo, hi := s.Index.FindRowOffset(s.FirstRow), s.Index.FindRowOffset(s.LastRow+1)
		positions = func(yield func(int) bool) {
			for i := lo; i < hi; i++ {
				if !yield(i) {
					return
				}
			}
		}
	}
	next := s.FirstRow
	selected := 0
	err := wb.VisitRowsWithOptionsContext(ctx, s.Index, positions, s.FirstCol, s.LastCol, options, func(row workbook.RowData) error {
		if s.RowNumbers != nil {
			for selected < len(s.RowNumbers) && s.RowNumbers[selected] != row.Number {
				if err := output(s.RowNumbers[selected], workbook.RowData{Number: s.RowNumbers[selected]}); err != nil {
					return err
				}
				selected++
			}
			selected++
		}
		if !s.Filtered {
			for next < row.Number {
				if err := output(next, workbook.RowData{Number: next}); err != nil {
					return err
				}
				next++
			}
			next = row.Number + 1
		}
		return output(row.Number, row)
	})
	if err != nil {
		return err
	}
	for s.RowNumbers != nil && selected < len(s.RowNumbers) {
		number := s.RowNumbers[selected]
		if err := output(number, workbook.RowData{Number: number}); err != nil {
			return err
		}
		selected++
	}
	if !s.Filtered {
		for next <= s.LastRow {
			if err := output(next, workbook.RowData{Number: next}); err != nil {
				return err
			}
			next++
		}
	}
	reportProgress(true)
	return nil
}

func textOf(c workbook.CellData, mode string) string {
	if mode == "formula" && c.HasFormula {
		return c.Formula
	}
	if mode == "display" {
		return c.Display
	}
	return c.Raw
}

// ProtectCSVText prefixes formula-like text when the caller explicitly enables
// CSV text protection. Numeric source cells and requested formulas bypass it.
func ProtectCSVText(value string) string {
	return separated.ProtectText(value)
}
func safeTabularValue(c workbook.CellData, opts Options) string {
	value := textOf(c, opts.Mode)
	// Keep actual numeric values and explicitly requested formulas intact.
	numeric := (c.Type == "n" || c.Type == "") && isNumeric(value)
	if opts.ProtectCSV && !(opts.Mode == "formula" && c.HasFormula) && !numeric {
		return ProtectCSVText(value)
	}
	return value
}
func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return (err == nil || errors.Is(err, strconv.ErrRange)) && s != ""
}

func writeSeparated(ctx context.Context, dst io.Writer, wb *workbook.Workbook, s Selection, opts Options, report Tick) error {
	out := bufio.NewWriterSize(dst, 256<<10)
	if err := writeSeparatedHead(out, opts); err != nil {
		return err
	}
	emit := separatedRowWriter(out, s, opts)
	if err := forEachRow(ctx, wb, s, emit, report); err != nil {
		return err
	}
	return out.Flush()
}

// encoding/csv's UseCRLF also removes lone CRs INSIDE quoted fields. Keep
// Windows record separators while preserving every source field byte.
func writeSeparatedRecord(out *bufio.Writer, fields []string, comma byte) error {
	for i, field := range fields {
		if i > 0 {
			if err := out.WriteByte(comma); err != nil {
				return err
			}
		}
		if err := separated.WriteField(out, field, comma, false); err != nil {
			return err
		}
	}
	_, err := out.WriteString("\r\n")
	return err
}

type cellRecord struct {
	SourceType   string `json:"sourceType,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Ref          string `json:"ref"`
	Value        string `json:"value"`
	Raw          string `json:"raw,omitempty"`
	Formula      string `json:"formula,omitempty"`
	Style        int    `json:"style,omitempty"`
	Type         string `json:"type,omitempty"`
	MissingCache bool   `json:"cacheMissing,omitempty"`
}

func writeJSONL(ctx context.Context, dst io.Writer, wb *workbook.Workbook, s Selection, opts Options, report Tick) error {
	out := bufio.NewWriterSize(dst, 256<<10)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	if err := forEachRow(ctx, wb, s, jsonlRowWriter(enc, s, opts), report); err != nil {
		return err
	}
	return out.Flush()
}
func writeHTML(ctx context.Context, dst io.Writer, wb *workbook.Workbook, s Selection, opts Options, report Tick) error {
	out := bufio.NewWriterSize(dst, 256<<10)
	if err := writeHTMLHead(out, wb, s, opts); err != nil {
		return err
	}
	var incomplete int64
	err := forEachRowWithOptions(ctx, wb, s, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: opts.Styles, ConditionalFormats: opts.Styles}, func(_ int, outRow int, row workbook.RowData) error {
		count, err := writeHTMLRow(out, wb, s, opts, outRow, row)
		incomplete += int64(count)
		return err
	}, report)
	if err != nil {
		return err
	}
	if err := writeHTMLTail(out, incomplete); err != nil {
		return err
	}
	return out.Flush()
}
func cssColor(s string) string {
	if len(s) == 7 && s[0] == '#' {
		for _, c := range s[1:] {
			if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
				return ""
			}
		}
		return s
	}
	return ""
}

func writeCSVArchive(ctx context.Context, dst io.Writer, wb *workbook.Workbook, selections []Selection, opts Options, report Tick) error {
	zw := zip.NewWriter(dst)
	names := csvArchiveNames(selections)
	for i, s := range selections {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, err := zw.Create(names[i])
		if err != nil {
			return err
		}
		csv := opts
		csv.Format = "csv"
		if err := writeSeparated(ctx, item, wb, s, csv, report); err != nil {
			return err
		}
	}
	body, err := csvArchiveManifest(wb.DisplayName, wb.Fingerprint, selections, opts, names, time.Now())
	if err != nil {
		return err
	}
	f, err := zw.Create("_ExcelLens_manifest.json")
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	return zw.Close()
}
func safePartName(name string) string {
	n := strings.Map(func(r rune) rune {
		if strings.ContainsRune("\\/:*?\"<>|", r) || r < 32 {
			return '_'
		}
		return r
	}, name)
	n = strings.Trim(n, " .")
	if n == "" {
		return "Sheet"
	}
	if len([]rune(n)) > 72 {
		n = string([]rune(n)[:72])
	}
	stem := strings.ToUpper(strings.SplitN(n, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9", "COM¹", "COM²", "COM³", "LPT¹", "LPT²", "LPT³":
		n = "_" + n
	}
	return n
}

// quiet compile-time interface check; XLSX uses XML escaping at the cell level.
var _ = xml.EscapeText
