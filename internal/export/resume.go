package export

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Resume writes only unconfirmed complete records/parts to a private journal.
// It does not publish a file; AssembleResume reconstitutes a complete output.
func Resume(ctx context.Context, wb *workbook.Workbook, selections []Selection, opts Options, journal ResumeJournal, progress Progress) error {
	if err := Validate(opts, selections); err != nil {
		return err
	}
	state := journal.State()
	if err := ValidateResumeState(state, opts, selections); err != nil {
		return err
	}
	var total int64
	for _, selection := range selections {
		total += selection.RowCount()
	}
	var prepared *xlsxWriteContext
	if opts.Format == "xlsx" && state.Phase != "done" {
		var err error
		prepared, err = prepareXLSX(ctx, wb, selections, opts)
		if err != nil {
			return err
		}
		if state.Sheet > 0 {
			found := false
			if err := journal.Visit(ctx, func(part ResumePart, source io.Reader) error {
				if part.Before.Sheet != state.Sheet-1 {
					_, err := io.Copy(io.Discard, source)
					return err
				}
				palette, err := readResumePalette(ctx, source)
				if err != nil {
					return err
				}
				prepared.palette, found = palette, true
				return requirePartEOF(source)
			}); err != nil {
				return err
			}
			if !found {
				return ErrResumeState
			}
		}
	}
	for state.Phase != "done" {
		if err := ctx.Err(); err != nil {
			return err
		}
		part := ResumePart{Before: state}
		s := selections[min(state.Sheet, len(selections)-1)]
		switch state.Phase {
		case "text", "html-rows":
			part.Count = min(int64(formats.ExportRecoveryRows), s.RowCount()-state.Row)
		case "xlsx-sheet", "zipcsv-sheet":
			part.Count = s.RowCount()
		}
		base, pending := ResumeDoneRows(state, selections), int64(0)
		tick := func(n int64, stage string) {
			pending += n
			if progress != nil {
				progress(base+pending, total, stage)
			}
		}
		if err := journal.Commit(ctx, part, func(dst io.Writer) (ResumeState, error) {
			var notices int64
			var err error
			switch state.Phase {
			case "text", "html-rows":
				notices, err = writeResumeWindow(ctx, dst, wb, s, state.Row, part.Count, opts)
			case "html-head":
				err = writeHTMLHead(dst, wb, s, opts)
			case "html-tail":
				err = writeHTMLTail(dst, state.Notices)
			case "xlsx-sheet":
				err = writeResumeXLSXPart(ctx, dst, wb, s, opts, tick, prepared.palette)
			case "zipcsv-sheet":
				csv := opts
				csv.Format = "csv"
				err = writeSeparated(ctx, dst, wb, s, csv, tick)
			default:
				err = ErrResumeState
			}
			if err != nil {
				return state, err
			}
			return NextResumeState(part, notices, opts, selections)
		}); err != nil {
			return err
		}
		state = journal.State()
		if err := ValidateResumeState(state, opts, selections); err != nil {
			return err
		}
		if progress != nil {
			progress(ResumeDoneRows(state, selections), total, fmt.Sprintf("%s · 已确认", s.Sheet.Name))
		}
	}
	return ctx.Err()
}

func writeResumeWindow(ctx context.Context, dst io.Writer, wb *workbook.Workbook, s Selection, offset, count int64, opts Options) (int64, error) {
	out := bufio.NewWriterSize(dst, 256<<10)
	var emit func(int, int, workbook.RowData) error
	var notices int64
	read := workbook.RowReadOptions{FormatValue: true, IncludeFormula: true}
	switch opts.Format {
	case "csv", "tsv":
		head := opts
		head.BOM = opts.BOM && offset == 0
		if err := writeSeparatedHead(out, head); err != nil {
			return 0, err
		}
		emit = separatedRowWriter(out, s, opts)
	case "jsonl":
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		emit = jsonlRowWriter(enc, s, opts)
	case "html":
		read.RichText, read.ConditionalFormats = opts.Styles, opts.Styles
		emit = func(_ int, output int, row workbook.RowData) error {
			n, err := writeHTMLRow(out, wb, s, opts, output, row)
			notices += int64(n)
			return err
		}
	default:
		return 0, ErrResumeState
	}
	if err := forEachRowWindow(ctx, wb, s, offset, count, read, emit); err != nil {
		return 0, err
	}
	return notices, out.Flush()
}

func requirePartEOF(source io.Reader) error {
	var extra [1]byte
	n, err := source.Read(extra[:])
	if n != 0 || err != io.EOF {
		if err != nil && err != io.EOF {
			return err
		}
		return ErrResumeState
	}
	return nil
}
