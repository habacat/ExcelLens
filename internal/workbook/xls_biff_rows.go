package workbook

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
)

func xlsRK(v uint32) float64 {
	var f float64
	if v&2 != 0 {
		f = float64(int32(v) >> 2)
	} else {
		f = math.Float64frombits(uint64(v&0xFFFFFFFC) << 32)
	}
	if v&1 != 0 {
		f /= 100
	}
	return f
}

func xlsNumber(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "#NUM!"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func xlsError(code byte) string {
	switch code {
	case 0:
		return "#NULL!"
	case 7:
		return "#DIV/0!"
	case 15:
		return "#VALUE!"
	case 23:
		return "#REF!"
	case 29:
		return "#NAME?"
	case 36:
		return "#NUM!"
	case 42:
		return "#N/A"
	case 43:
		return "#GETTING_DATA"
	default:
		return fmt.Sprintf("#ERROR(%d)", code)
	}
}

func (w *Workbook) streamXLSRows(meta SheetMeta, writer *externalRowWriter, cancel <-chan struct{}, progress func(int64, int64, string), catalogue *biffTableCapture) error {
	sheet, e := w.openBIFFWorksheet(meta)
	if e != nil {
		return e
	}
	defer sheet.cf.Close()
	sheet.cancel = cancel
	current := 0
	cells := make([]externalCell, 0, 64)
	maxCol := 0
	pendingString := false
	flush := func(target int) error {
		for current < target {
			if err := writer.cells(cells, maxCol); err != nil {
				return err
			}
			current++
			cells = cells[:0]
			maxCol = 0
		}
		return nil
	}
	add := func(r, col, xf int, typ byte, raw, formula string, hasFormula, missing bool) error {
		if r >= 65536 || col >= 256 {
			return fmt.Errorf("BIFF8 单元格越界 R%dC%d", r+1, col+1)
		}
		if r < current {
			return fmt.Errorf("XLS 工作表包含乱序单元格：第 %d 行位于第 %d 行之后", r+1, current)
		}
		if r > current {
			if err := flush(r); err != nil {
				return err
			}
		}
		if col+1 > maxCol {
			maxCol = col + 1
		}
		if len(cells) > 0 && col+1 <= cells[len(cells)-1].Col {
			// A duplicate cell record is legal in a few producer variants.
			// Keeping the last record matches the saved worksheet value.
			i := sort.Search(len(cells), func(i int) bool { return cells[i].Col >= col+1 })
			if i < len(cells) && cells[i].Col == col+1 {
				cells[i] = externalCell{Col: col + 1, Style: xf, Type: typ, Raw: raw, Formula: formula, HasFormula: hasFormula, CacheMissing: missing}
				return nil
			}
			cells = append(cells, externalCell{})
			copy(cells[i+1:], cells[i:])
			cells[i] = externalCell{Col: col + 1, Style: xf, Type: typ, Raw: raw, Formula: formula, HasFormula: hasFormula, CacheMissing: missing}
			return nil
		}
		cells = append(cells, externalCell{Col: col + 1, Style: xf, Type: typ, Raw: raw, Formula: formula, HasFormula: hasFormula, CacheMissing: missing})
		return nil
	}
	lastProgressRows := -1
	for {
		id, data, err := sheet.next()
		if err != nil {
			return err
		}
		if cancel != nil {
			select {
			case <-cancel:
				return errors.New("读取已取消")
			default:
			}
		}
		if catalogue != nil {
			catalogue.capture(id, data)
		}
		if id == 0x000A {
			if len(cells) > 0 {
				if err := flush(current + 1); err != nil {
					return err
				}
			}
			return nil
		}
		if progress != nil && writer.rows > 0 && writer.rows%4096 == 0 && writer.rows != lastProgressRows {
			lastProgressRows = writer.rows
			progress(sheet.reader.pos, sheet.reader.end, fmt.Sprintf("正在读取 XLS 第 %d 行", writer.rows))
		}
		if pendingString {
			if id == 0x0207 {
				if s, _, e := xlsInlineUnicode(data, false); e == nil && len(cells) > 0 {
					cells[len(cells)-1].Raw = s
					cells[len(cells)-1].Type = 's'
					cells[len(cells)-1].CacheMissing = false
				}
			}
			pendingString = false
		}
		if len(data) < 6 {
			continue
		}
		row := int(xlsUint16(data, 0))
		col := int(xlsUint16(data, 2))
		xf := int(xlsUint16(data, 4))
		switch id {
		case 0x00FD: // LABELSST
			if len(data) < 10 {
				return errors.New("LABELSST 记录截断")
			}
			index := int(xlsUint32(data, 6))
			if index < 0 || index >= len(w.Shared) {
				return errors.New("XLS 共享字符串索引无效")
			}
			if err := add(row, col, xf, 's', w.Shared[index], "", false, false); err != nil {
				return err
			}
		case 0x0203: // NUMBER
			if len(data) < 14 {
				return errors.New("NUMBER 记录截断")
			}
			if err := add(row, col, xf, 'n', xlsNumber(xlsFloat64(data, 6)), "", false, false); err != nil {
				return err
			}
		case 0x027E: // RK
			if len(data) < 10 {
				return errors.New("RK 记录截断")
			}
			if err := add(row, col, xf, 'n', xlsNumber(xlsRK(xlsUint32(data, 6))), "", false, false); err != nil {
				return err
			}
		case 0x00BD: // MULRK
			if len(data) < 12 || ((len(data)-6)%6) != 0 {
				return errors.New("MULRK 记录长度无效")
			}
			firstCol := int(xlsUint16(data, 2))
			lastCol := int(xlsUint16(data, len(data)-2))
			if lastCol-firstCol+1 != (len(data)-6)/6 {
				return errors.New("MULRK 列范围不匹配")
			}
			for i := firstCol; i <= lastCol; i++ {
				at := 4 + (i-firstCol)*6
				if err := add(row, i, int(xlsUint16(data, at)), 'n', xlsNumber(xlsRK(xlsUint32(data, at+2))), "", false, false); err != nil {
					return err
				}
			}
		case 0x0201: // BLANK, styles on empty cells carry borders/fills.
			if err := add(row, col, xf, 's', "", "", false, false); err != nil {
				return err
			}
		case 0x00BE: // MULBLANK, XF per blank cell.
			if len(data) < 8 || (len(data)-6)%2 != 0 {
				return errors.New("MULBLANK 长度无效")
			}
			first := int(xlsUint16(data, 2))
			last := int(xlsUint16(data, len(data)-2))
			if last-first+1 != (len(data)-6)/2 {
				return errors.New("MULBLANK 列范围不匹配")
			}
			for c := first; c <= last; c++ {
				if err := add(row, c, int(xlsUint16(data, 4+2*(c-first))), 's', "", "", false, false); err != nil {
					return err
				}
			}
		case 0x0205: // BOOLERR
			if len(data) < 8 {
				return errors.New("BOOLERR 截断")
			}
			typ, value := 'b', "0"
			if data[7] != 0 {
				typ = 'e'
				value = xlsError(data[6])
			} else if data[6] != 0 {
				value = "1"
			}
			if err := add(row, col, xf, byte(typ), value, "", false, false); err != nil {
				return err
			}
		case 0x0204, 0x00D6: // LABEL, RSTRING
			if len(data) < 9 {
				return errors.New("LABEL 截断")
			}
			s, _, e := xlsInlineUnicode(data[6:], false)
			if e != nil {
				return e
			}
			if err := add(row, col, xf, 's', s, "", false, false); err != nil {
				return err
			}
		case 0x0006: // FORMULA
			if len(data) < 22 {
				return errors.New("FORMULA 截断")
			}
			raw := ""
			typ := byte('n')
			missing := false
			cache := data[6:14]
			if cache[6] == 0xFF && cache[7] == 0xFF {
				switch cache[0] {
				case 0:
					missing = true
					typ = 's'
					pendingString = true
				case 1:
					typ = 'b'
					if cache[2] != 0 {
						raw = "1"
					} else {
						raw = "0"
					}
				case 2:
					typ = 'e'
					raw = xlsError(cache[2])
				default:
					missing = true
					typ = 's'
				}
			} else {
				raw = xlsNumber(xlsFloat64(cache, 0))
			}
			tokLen := int(xlsUint16(data, 20))
			if tokLen > len(data)-22 {
				return errors.New("FORMULA 令牌截断")
			}
			formula := decodeBIFFFormula(data[22:22+tokLen], row, col)
			if err := add(row, col, xf, typ, raw, formula, true, missing); err != nil {
				return err
			}
		}
	}
}
