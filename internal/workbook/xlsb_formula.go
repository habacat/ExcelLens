package workbook

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

type xlsbFormula struct {
	tokens, extra []byte
}

func (f *xlsbFields) formula() xlsbFormula {
	n := int(f.u32())
	if n > formats.BIFFFormulaBytes && f.err == nil {
		f.err = errors.New("BIFF12 公式令牌超过读取容量")
	}
	tokens := f.take(n)
	return xlsbFormula{tokens, f.take(int(f.u32()))}
}

func (a xlsbFormula) copy() xlsbFormula {
	return xlsbFormula{append([]byte(nil), a.tokens...), append([]byte(nil), a.extra...)}
}

func (a xlsbFormula) pointer() (int, int, bool) {
	if len(a.tokens) != 5 || a.tokens[0] != 1 || len(a.extra) != 4 {
		return 0, 0, false
	}
	row, col := binary.LittleEndian.Uint32(a.tokens[1:]), binary.LittleEndian.Uint32(a.extra)
	return int(row), int(col), row < formats.ExcelRows && col < formats.ExcelColumns
}

func (b *xlsbBook) formulaText(formula xlsbFormula, atRow, atCol int, relative3D bool) (string, bool) {
	src := xlsbFields{data: formula.tokens}
	extra := xlsbFields{data: formula.extra}
	stack := make([]string, 0, 12)
	work := 0
	var token byte
	bad := func() (string, bool) {
		return fmt.Sprintf("[XLSB 公式无法还原，BIFF12 令牌 %02X]", token), false
	}
	if len(src.data) == 0 {
		return "[XLSB 公式没有令牌]", false
	}
	take := func(n int) []string {
		if n > len(stack) {
			src.err = errors.New("BIFF12 公式操作数不足")
			return nil
		}
		i := len(stack) - n
		args := stack[i:]
		stack = stack[:i]
		return args
	}
	for len(src.data) > 0 && src.err == nil {
		token = src.u8()
		kind := token
		if kind&0x80 != 0 {
			return bad()
		}
		if kind >= 0x20 {
			kind = (kind & 31) | 32
		}
		var text string
		if kind >= 3 && kind <= 17 {
			op := excelBinaryOperators[kind-3]
			args := take(2)
			if len(args) != 2 {
				return bad()
			}
			text = "(" + args[0] + op + args[1] + ")"
		} else {
			switch kind {
			case 0x12, 0x13, 0x14, 0x15:
				args := take(1)
				if len(args) != 1 {
					return bad()
				}
				switch kind {
				case 0x12:
					text = "+" + args[0]
				case 0x13:
					text = "-" + args[0]
				case 0x14:
					text = args[0] + "%"
				case 0x15:
					text = "(" + args[0] + ")"
				}
			case 0x16: // missing function argument
			case 0x17:
				text = xlsbQuoteString(src.utf16(uint32(src.u16()), 255))
			case 0x18:
				if src.u8() != 0x19 { // PtgList
					return bad()
				}
				var ok bool
				text, ok = b.structuredRef(&src)
				if !ok {
					return bad()
				}
			case 0x19:
				attr, value := src.u8(), src.u16()
				switch attr {
				case 0x01, 0x02, 0x08, 0x40, 0x41, 0x80: // volatility, control and display-only tokens
					continue
				case 0x04: // CHOOSE jump table
					src.take((int(value) + 1) * 2)
					continue
				case 0x10: // optimized SUM
					args := take(1)
					if len(args) != 1 {
						return bad()
					}
					text = "SUM(" + args[0] + ")"
				default:
					return bad()
				}
			case 0x1C:
				text = xlsbErrorValue(src.u8())
				if strings.HasPrefix(text, "#ERROR(") {
					return bad()
				}
			case 0x1D:
				if v := src.u8(); v < 2 {
					text = [...]string{"FALSE", "TRUE"}[v]
				} else {
					return bad()
				}
			case 0x1E:
				text = strconv.Itoa(int(src.u16()))
			case 0x1F:
				text = xlsNumber(src.f64())
			case 0x20:
				src.take(14)
				text = xlsbArrayLiteral(&extra)
			case 0x21, 0x22:
				n := 0
				if kind == 0x22 {
					n = int(src.u8())
				}
				id := src.u16()
				name := xlsFunctionNames[id]
				if name == "" {
					name = xlsbFunctionNames[id]
				}
				if kind == 0x21 {
					var ok bool
					n, ok = xlsbFixedArities[id]
					if !ok {
						n, ok = xlsFixedArity[id]
					}
					if !ok {
						return bad()
					}
				}
				args := take(n)
				if src.err != nil {
					return bad()
				}
				if id == 255 && len(args) > 0 {
					name, args = args[0], args[1:]
				}
				if name == "" {
					return bad()
				}
				text = name + "(" + strings.Join(args, ",") + ")"
			case 0x23:
				n := src.u32()
				if n == 0 || n > uint32(len(b.names)) || b.names[n-1].name == "" {
					return bad()
				}
				item := b.names[n-1]
				text = item.name
				if item.scope < uint32(len(b.sheetNames)) {
					text = xlsbSheetPrefix(b.sheetNames[item.scope]) + text
				} else if item.scope != math.MaxUint32 {
					return bad()
				}
			case 0x39: // PtgNameX indexes names in an external link part.
				return "[XLSB 外部工作簿的名称引用无法还原]", false
			case 0x24, 0x25, 0x2C, 0x2D, 0x3A, 0x3B:
				prefix := ""
				offsets := kind == 0x2C || kind == 0x2D
				if kind >= 0x3A {
					var ok bool
					prefix, ok = b.sheetPrefix(src.u16())
					if !ok {
						return bad()
					}
					offsets = relative3D
				}
				var ok bool
				if kind == 0x25 || kind == 0x2D || kind == 0x3B {
					text, ok = xlsbAreaRef(&src, atRow, atCol, offsets)
				} else {
					row, col := src.u32(), src.u16()
					var r, c string
					r, c, ok = xlsbRefParts(row, col, atRow, atCol, offsets)
					text = c + r
				}
				if !ok {
					return bad()
				}
				text = prefix + text
			case 0x26: // PtgMemArea also consumes its range cache in RgbExtra.
				src.take(6)
				n := extra.u32()
				if n > uint32(len(extra.data)/16) {
					return bad()
				}
				extra.take(int(n) * 16)
				continue
			case 0x27, 0x28:
				src.take(6)
				continue
			case 0x29:
				src.take(2)
				continue
			case 0x2A, 0x2B, 0x3C, 0x3D:
				if kind >= 0x3C {
					src.take(2)
				}
				if kind == 0x2B || kind == 0x3D {
					src.take(12)
				} else {
					src.take(6)
				}
				text = "#REF!"
			default:
				return bad()
			}
		}
		work += len(text)
		if len(text) > formats.BIFFFormulaBytes || work > formats.BIFFFormulaWork || len(stack) > 1024 {
			return bad()
		}
		stack = append(stack, text)
	}
	if src.err != nil || extra.err != nil || len(extra.data) != 0 || len(stack) != 1 || stack[0] == "" {
		return bad()
	}
	return "=" + stack[0], true
}

func xlsbRefParts(rawRow uint32, rawCol uint16, row, col int, offsets bool) (string, string, bool) {
	rr := int(rawRow)
	cc := int(rawCol & 0x3FFF)
	relRow, relCol := rawCol&0x8000 != 0, rawCol&0x4000 != 0
	if offsets && relRow {
		if row < 0 {
			return "", "", false
		}
		rr = (row + int(rawRow&0xFFFFF)) & (formats.ExcelRows - 1)
	}
	if offsets && relCol {
		if col < 0 {
			return "", "", false
		}
		cc = (col + cc) & (formats.ExcelColumns - 1)
	}
	if rr >= formats.ExcelRows || cc >= formats.ExcelColumns {
		return "", "", false
	}
	r, c := strconv.Itoa(rr+1), ColumnName(cc+1)
	if !relRow {
		r = "$" + r
	}
	if !relCol {
		c = "$" + c
	}
	return r, c, true
}

func xlsbAreaRef(f *xlsbFields, row, col int, offsets bool) (string, bool) {
	r1, r2, c1, c2 := f.u32(), f.u32(), f.u16(), f.u16()
	rStart, cStart, ok1 := xlsbRefParts(r1, c1, row, col, offsets)
	rEnd, cEnd, ok2 := xlsbRefParts(r2, c2, row, col, offsets)
	switch {
	case !offsets && r1 == 0 && r2 == formats.ExcelRows-1:
		return cStart + ":" + cEnd, ok1 && ok2
	case !offsets && c1&0x3FFF == 0 && c2&0x3FFF == formats.ExcelColumns-1:
		return rStart + ":" + rEnd, ok1 && ok2
	default:
		return cStart + rStart + ":" + cEnd + rEnd, ok1 && ok2
	}
}

func (b *xlsbBook) sheetPrefix(index uint16) (string, bool) {
	if int(index) >= len(b.externals) {
		return "", false
	}
	xti := b.externals[index]
	if xti.source >= uint32(len(b.supports)) || !b.supports[xti.source] ||
		xti.first < 0 || xti.last < xti.first || int(xti.last) >= len(b.sheetNames) {
		return "", false
	}
	name := b.sheetNames[xti.first]
	if xti.first != xti.last {
		name += ":" + b.sheetNames[xti.last]
	}
	return xlsbSheetPrefix(name), true
}

func xlsbSheetPrefix(name string) string {
	return "'" + strings.ReplaceAll(name, "'", "''") + "'!"
}

func (b *xlsbBook) structuredRef(f *xlsbFields) (string, bool) {
	f.u16() // XTI; a resident table is verified by its workbook-unique ID.
	flags := f.u16()
	id := f.u32()
	first, last := int(f.u16()), int(f.u16())
	table := b.tables[id]
	if flags&(3<<12) != 0 || table == nil {
		return "", false
	}
	escape := strings.NewReplacer("'", "''", "[", "'[", "]", "']", "#", "'#", "@", "'@")
	ref := ""
	switch flags & 3 {
	case 1:
		if first >= len(table.names) {
			return "", false
		}
		ref = "[" + escape.Replace(table.names[first]) + "]"
	case 2:
		if first > last || last >= len(table.names) {
			return "", false
		}
		ref = "[" + escape.Replace(table.names[first]) + "]:[" + escape.Replace(table.names[last]) + "]"
	case 3:
		return "", false
	}
	row := (flags >> 2) & 31
	if row == 0 || row == 4 {
		if ref == "" {
			return table.display, true
		}
		if flags&3 == 1 {
			return table.display + ref, true
		}
		return table.display + "[" + ref + "]", true
	}
	part := map[uint16]string{1: "[#All]", 2: "[#Headers]", 6: "[#Headers],[#Data]", 8: "[#Totals]", 12: "[#Data],[#Totals]", 16: "[#This Row]"}[row]
	if part == "" {
		return "", false
	}
	if ref != "" {
		part += "," + ref
	}
	return table.display + "[" + part + "]", true
}

func xlsbArrayLiteral(f *xlsbFields) string {
	rows, cols := f.u32(), f.u32()
	if rows == 0 || cols == 0 || uint64(rows)*uint64(cols) > 4096 {
		f.err = errors.New("BIFF12 公式数组超限")
		return ""
	}
	var text strings.Builder
	text.WriteByte('{')
	for row := uint32(0); row < rows && f.err == nil; row++ {
		if row != 0 {
			text.WriteByte(';')
		}
		for col := uint32(0); col < cols && f.err == nil; col++ {
			if col != 0 {
				text.WriteByte(',')
			}
			switch f.u8() {
			case 0:
				text.WriteString(xlsNumber(f.f64()))
			case 1:
				text.WriteString(xlsbQuoteString(f.utf16(uint32(f.u16()), 32767)))
			case 2:
				v := f.u8()
				if v > 1 {
					f.err = errors.New("BIFF12 公式数组布尔值无效")
				} else if v == 1 {
					text.WriteString("TRUE")
				} else {
					text.WriteString("FALSE")
				}
			case 4:
				value := xlsbErrorValue(f.u8())
				if strings.HasPrefix(value, "#ERROR(") {
					f.err = errors.New("BIFF12 公式数组错误值无效")
				}
				text.WriteString(value)
				f.take(3)
			default:
				f.err = errors.New("BIFF12 公式数组类型未支持")
			}
			if text.Len() > formats.BIFFFormulaBytes {
				f.err = errors.New("BIFF12 公式数组内容超限")
			}
		}
	}
	text.WriteByte('}')
	return text.String()
}

func xlsbQuoteString(value string) string {
	return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
}

func xlsbErrorValue(code byte) string {
	return xlsError(code)
}

func xlsbNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func (w *Workbook) resolveXLSBNames() {
	b := w.source.xlsb
	w.NamedRanges = make(map[string]string)
	w.LocalNamedRanges = make(map[string]map[string]string)
	for i := range b.names {
		item := &b.names[i]
		item.definition, _ = b.formulaText(item.formula, -1, -1, false)
		item.formula = xlsbFormula{}
		name := item.name
		definition := ""
		if strings.HasPrefix(item.definition, "=") {
			definition = strings.TrimPrefix(item.definition, "=")
		}
		if item.flags&32 != 0 {
			name = "_xlnm." + name
		}
		scope := ""
		if item.scope != math.MaxUint32 {
			if item.scope >= uint32(len(b.sheetNames)) {
				continue
			}
			scope = b.sheetNames[item.scope]
		}
		registerWorkbookName(w.NamedRanges, w.LocalNamedRanges, scope, name, definition)
	}
}
