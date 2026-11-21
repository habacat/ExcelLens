package export

import (
	"bufio"
	"encoding/json"

	"excellens/internal/workbook"
)

func writeSeparatedHead(out *bufio.Writer, opts Options) error {
	if opts.BOM && opts.Format == "csv" {
		_, err := out.Write([]byte{0xef, 0xbb, 0xbf})
		return err
	}
	return nil
}

func separatedRowWriter(out *bufio.Writer, s Selection, opts Options) func(int, int, workbook.RowData) error {
	fields := make([]string, s.LastCol-s.FirstCol+1)
	comma := byte(',')
	if opts.Format == "tsv" {
		comma = '\t'
	}
	return func(_, _ int, row workbook.RowData) error {
		clear(fields)
		for _, cell := range row.Cells {
			if cell.Col >= s.FirstCol && cell.Col <= s.LastCol {
				fields[cell.Col-s.FirstCol] = safeTabularValue(cell, opts)
			}
		}
		return writeSeparatedRecord(out, fields, comma)
	}
}

func jsonlRowWriter(enc *json.Encoder, s Selection, opts Options) func(int, int, workbook.RowData) error {
	return func(original, output int, row workbook.RowData) error {
		cells := make([]cellRecord, 0, len(row.Cells))
		for _, c := range row.Cells {
			if c.Raw == "" && !c.HasFormula && c.SourceType == "" {
				continue
			}
			cells = append(cells, cellRecord{Ref: c.Ref, SourceType: c.SourceType, Currency: c.Currency, Value: textOf(c, opts.Mode), Raw: c.Raw, Formula: c.Formula, Style: c.Style, Type: c.Type, MissingCache: c.CacheMissing})
		}
		if len(cells) == 0 {
			return nil
		}
		return enc.Encode(map[string]any{"sheet": s.Sheet.Name, "sourceRow": original, "outputRow": output, "cells": cells})
	}
}
