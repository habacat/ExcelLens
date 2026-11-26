package workbook

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"

	"excellens/internal/formats"
)

type conditionalInput struct {
	source    io.Reader
	ctx       context.Context
	remaining *int
}

func (in conditionalInput) Read(p []byte) (int, error) {
	if err := in.ctx.Err(); err != nil {
		return 0, err
	}
	if *in.remaining <= 0 {
		return 0, ErrPreviewUnavailable
	}
	n, err := in.source.Read(p[:min(len(p), *in.remaining)])
	*in.remaining -= n
	return n, err
}

func (r *conditionalRead) readForeignPrefix(meta SheetMeta, row, col int) (CellData, error) {
	wrap := func(src io.Reader) io.Reader { return conditionalInput{src, r.ctx, &r.dependencyBytes} }
	if r.w.Format == "xlsb" {
		if err := r.w.CheckSourceUnchanged(); err != nil {
			return CellData{}, err
		}
		var value CellData
		_, err := r.w.walkXLSBRows(meta, row, col, r.ctx.Done(), nil, wrap, func(n, style int, cells []externalCell) error {
			if n != row {
				return nil
			}
			for _, c := range cells {
				if c.Col == col {
					value = r.w.externalCellValue(strconv.Itoa(n), c, false)
					break
				}
			}
			return errPreviewDone
		})
		if errors.Is(err, errPreviewDone) {
			err = nil
		}
		return value, err
	}
	archive, err := zip.OpenReader(r.w.Path)
	if err != nil {
		return CellData{}, err
	}
	defer archive.Close()
	if len(archive.File) > maxArchiveEntries || fingerprintZip(archive.File) != r.w.Fingerprint {
		return CellData{}, errors.New("来源已改变，请重新打开工作簿")
	}
	for _, part := range archive.File {
		if path.Clean(part.Name) != meta.Path {
			continue
		}
		input, err := part.Open()
		if err != nil {
			return CellData{}, err
		}
		defer input.Close()
		return r.w.readConditionalXMLCell(xml.NewDecoder(wrap(input)), row, col)
	}
	return CellData{}, errors.New("缺少目标工作表部件")
}

// Stop at a proven gap or the requested physical row. Truncation or a byte
// limit must not turn an unread dependency into a blank.
func (w *Workbook) readConditionalXMLCell(d *xml.Decoder, row, col int) (CellData, error) {
	var root, data xml.Name
	var previous int
	for {
		token, err := d.Token()
		if err != nil {
			return CellData{}, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch {
			case root.Local == "":
				if t.Name.Local != "worksheet" {
					return CellData{}, errors.New("工作表根节点无效")
				}
				root = t.Name
			case data.Local == "" && t.Name.Local == "sheetData" && t.Name.Space == root.Space:
				data = t.Name
			case data.Local != "" && t.Name.Local == "row" && t.Name.Space == data.Space:
				no := previous + 1
				for _, a := range t.Attr {
					if a.Name.Local == "r" && a.Name.Space == "" {
						no, err = strconv.Atoi(a.Value)
						if err != nil || no <= previous || no > formats.ExcelRows {
							return CellData{}, errors.New("工作表行坐标无效")
						}
					}
				}
				previous = no
				if no > row {
					return CellData{}, nil
				}
				if no == row {
					return w.readConditionalXMLRow(d, t.Name, row, col)
				}
				if err = d.Skip(); err != nil {
					return CellData{}, err
				}
			default:
				if err = d.Skip(); err != nil {
					return CellData{}, err
				}
			}
		case xml.EndElement:
			if data.Local != "" && t.Name == data {
				return CellData{}, nil
			}
			if t.Name == root {
				return CellData{}, errors.New("缺少工作表数据区")
			}
		}
	}
}

// Decode only the operand; a wide source row may contain large strings or
// rich runs in unrelated columns.
func (w *Workbook) readConditionalXMLRow(d *xml.Decoder, element xml.Name, row, col int) (CellData, error) {
	last := 0
	for {
		token, err := d.Token()
		if err != nil {
			return CellData{}, err
		}
		switch t := token.(type) {
		case xml.EndElement:
			if t.Name == element {
				return CellData{}, nil
			}
		case xml.StartElement:
			if t.Name.Local != "c" || t.Name.Space != element.Space {
				if err := d.Skip(); err != nil {
					return CellData{}, err
				}
				continue
			}
			at := last + 1
			for _, a := range t.Attr {
				if a.Name.Space == "" && a.Name.Local == "r" {
					var sourceRow int
					at, sourceRow, err = ParseCellRef(a.Value)
					if err != nil || sourceRow != row {
						return CellData{}, fmt.Errorf("源单元格地址无效 %q", a.Value)
					}
				}
			}
			if at < 1 || at > formats.ExcelColumns {
				return CellData{}, errors.New("源单元格列超限")
			}
			last = at
			if at != col {
				if err := d.Skip(); err != nil {
					return CellData{}, err
				}
				continue
			}
			var c xmlCell
			if err := d.DecodeElement(&c, &t); err != nil {
				return CellData{}, err
			}
			if c.Type == "s" {
				id, err := parseNonNegativeInt(c.Value)
				if err != nil || id >= len(w.Shared) {
					return CellData{}, errors.New("共享字符串索引无效")
				}
			}
			return w.resolveCell(c, col, nil, false, false), nil
		}
	}
}
