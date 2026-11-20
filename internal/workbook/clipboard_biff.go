package workbook

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"

	"excellens/internal/formats"
)

// Excel compacts cells in the embedded OOXML of a filtered selection but
// keeps the unfiltered formula addresses. BIFF8 ROW records carry the
// original row numbers; DIMENSIONS describes the compact destination.
func ClipboardSourceRows(ctx context.Context, data []byte, start, end, firstCol, lastCol, count int) ([]int, error) {
	c, err := parseCompound(bytes.NewReader(data), int64(len(data)), func(n string) bool { return n == "workbook" || n == "book" })
	if err != nil {
		return nil, err
	}
	var head [4]byte
	var body [16]byte
	inSheet, matched := false, false
	var rows []int
	for off := int64(0); off+4 <= c.workbook.size; {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if _, err = c.workbook.ReadAt(head[:], off); err != nil {
			return nil, err
		}
		id, size := binary.LittleEndian.Uint16(head[:2]), int(binary.LittleEndian.Uint16(head[2:]))
		off += 4
		if int64(size) > c.workbook.size-off {
			return nil, io.ErrUnexpectedEOF
		}
		switch id {
		case 0x0809, 0x0200, 0x0208:
			n := min(size, len(body))
			if _, err = c.workbook.ReadAt(body[:n], off); err != nil {
				return nil, err
			}
			switch id {
			case 0x0809:
				inSheet = size >= 4 && binary.LittleEndian.Uint16(body[2:]) == 0x10
				matched = false
				rows = nil
			case 0x0200:
				if inSheet && size >= 12 {
					lo, hi := int(binary.LittleEndian.Uint32(body[:])), int(binary.LittleEndian.Uint32(body[4:]))
					matched = lo == start-1 && hi-lo == count && int(binary.LittleEndian.Uint16(body[8:])) == firstCol-1 && int(binary.LittleEndian.Uint16(body[10:])) == lastCol
				}
			case 0x0208:
				if inSheet && matched && size >= 16 {
					r := int(binary.LittleEndian.Uint16(body[:])) + 1
					if r < start || r > end || len(rows) >= count || len(rows) >= formats.EditBatchCells || len(rows) > 0 && r <= rows[len(rows)-1] {
						return nil, errors.New("Excel 剪贴板源行顺序无效")
					}
					rows = append(rows, r)
				}
			}
		case 0x000a:
			if inSheet && matched && len(rows) == count {
				return rows, nil
			}
			inSheet, matched = false, false
		}
		off += int64(size)
	}
	return nil, errors.New("无法核对筛选后每一行的来源")
}
