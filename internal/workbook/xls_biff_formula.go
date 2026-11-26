package workbook

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// The formula reader does NOT evaluate expressions. Valid tokens for common
// operators, references and functions are reconstructed from RPN. Unsupported
// or external references are explicitly labelled, never invented.
func decodeBIFFFormula(tokens []byte, row, col int) string {
	if len(tokens) == 0 {
		return "[BIFF 公式无令牌]"
	}
	stack := make([]string, 0, 12)
	work := 0
	take := func() (string, bool) {
		if len(stack) == 0 {
			return "", false
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v, true
	}
	fail := func() string { return fmt.Sprintf("[BIFF公式令牌未支持: %X]", tokens) }
	for p := 0; p < len(tokens); {
		if len(stack) > 0 {
			work += len(stack[len(stack)-1])
			if len(stack[len(stack)-1]) > formats.BIFFFormulaBytes || work > formats.BIFFFormulaWork {
				return fail()
			}
		}
		kind := tokens[p]
		// Operand tokens have BIFF stack classes: R=0x20, V=0x40,
		// A=0x60. 0x44 and 0x64 must both decode as ptgRef(0x24),
		// not as unrelated tokens.
		if kind >= 0x60 {
			kind -= 0x40
		} else if kind >= 0x40 {
			kind -= 0x20
		}
		p++
		if kind >= 3 && kind <= 17 {
			op := excelBinaryOperators[kind-3]
			right, ok := take()
			if !ok {
				return fail()
			}
			left, ok := take()
			if !ok {
				return fail()
			}
			stack = append(stack, "("+left+op+right+")")
			continue
		}
		switch kind {
		case 0x12, 0x13, 0x14, 0x15:
			a, ok := take()
			if !ok {
				return fail()
			}
			switch kind {
			case 0x12:
				a = "+" + a
			case 0x13:
				a = "-" + a
			case 0x14:
				a = a + "%"
			case 0x15:
				a = "(" + a + ")"
			}
			stack = append(stack, a)
		case 0x1E:
			if p+2 > len(tokens) {
				return fail()
			}
			stack = append(stack, strconv.Itoa(int(xlsUint16(tokens, p))))
			p += 2
		case 0x1F:
			if p+8 > len(tokens) {
				return fail()
			}
			value := xlsFloat64(tokens, p)
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return fail()
			}
			stack = append(stack, xlsNumber(value))
			p += 8
		case 0x1D:
			if p >= len(tokens) {
				return fail()
			}
			if tokens[p] > 1 {
				return fail()
			}
			if tokens[p] != 0 {
				stack = append(stack, "TRUE")
			} else {
				stack = append(stack, "FALSE")
			}
			p++
		case 0x1C:
			if p >= len(tokens) {
				return fail()
			}
			stack = append(stack, xlsError(tokens[p]))
			p++
		case 0x17:
			if p+2 > len(tokens) {
				return fail()
			}
			n := int(tokens[p])
			p++
			flags := tokens[p]
			p++
			size := n
			if flags&1 != 0 {
				size *= 2
			}
			if p+size > len(tokens) {
				return fail()
			}
			text := ""
			if n > 0 {
				encoded := append([]byte{flags & 1}, tokens[p:p+size]...)
				var err error
				text, _, err = biffCatalogueString(encoded, n)
				if err != nil {
					return fail()
				}
			}
			stack = append(stack, "\""+strings.ReplaceAll(text, "\"", "\"\"")+"\"")
			p += size
		case 0x24, 0x2C: // PtgRef uses coordinates; PtgRefN uses relative offsets.
			if p+4 > len(tokens) {
				return fail()
			}
			ref, ok := biffFormulaCellRef(xlsUint16(tokens, p), xlsUint16(tokens, p+2), row, col, kind == 0x2C)
			p += 4
			if !ok {
				return fail()
			}
			stack = append(stack, ref)
		case 0x25, 0x2D: // PtgArea / PtgAreaN retain each corner's display anchors.
			if p+8 > len(tokens) {
				return fail()
			}
			left, lok := biffFormulaCellRef(xlsUint16(tokens, p), xlsUint16(tokens, p+4), row, col, kind == 0x2D)
			right, rok := biffFormulaCellRef(xlsUint16(tokens, p+2), xlsUint16(tokens, p+6), row, col, kind == 0x2D)
			p += 8
			if !lok || !rok {
				return fail()
			}
			stack = append(stack, left+":"+right)
		case 0x21, 0x22:
			argc := 0
			if kind == 0x22 {
				if p+3 > len(tokens) {
					return fail()
				}
				argc = int(tokens[p] & 0x7F)
				p++
			} else {
				if p+2 > len(tokens) {
					return fail()
				}
			}
			fn := xlsUint16(tokens, p)
			p += 2
			name, known := xlsFunctionNames[fn]
			if !known {
				return fail()
			}
			if kind == 0x21 {
				var fixed bool
				argc, fixed = xlsFixedArity[fn]
				if !fixed {
					return fail()
				}
			}
			if argc > len(stack) {
				return fail()
			}
			args := append([]string(nil), stack[len(stack)-argc:]...)
			stack = stack[:len(stack)-argc]
			stack = append(stack, name+"("+strings.Join(args, ",")+")")
		default:
			_ = row
			_ = col
			return fail()
		}
	}
	if len(stack) != 1 || len(stack[0]) > formats.BIFFFormulaBytes {
		return fail()
	}
	return "=" + stack[0]
}

var excelBinaryOperators = [...]string{"+", "-", "*", "/", "^", "&", "<", "<=", "=", ">=", ">", "<>", " ", ",", ":"}

// RgceLoc stores coordinates even when its flags request relative display.
// RgceLocRel (the N variants) stores signed offsets for relative components;
// MS-XLS specifies one source-domain adjustment at the row/column boundary.
func biffFormulaCellRef(rawRow, rawCol uint16, atRow, atCol int, offsets bool) (string, bool) {
	if offsets && (rawCol&0x8000 != 0 && (atRow < 0 || atRow >= formats.BIFFRows) || rawCol&0x4000 != 0 && (atCol < 0 || atCol >= formats.BIFFColumns)) {
		return "", false
	}
	row := int(rawRow)
	col := int(rawCol & 0x3FFF)
	if offsets && rawCol&0x8000 != 0 {
		row = atRow + int(int16(rawRow))
		if row < 0 {
			row += 65536
		} else if row >= 65536 {
			row -= 65536
		}
	}
	if offsets && rawCol&0x4000 != 0 {
		col = atCol + int(int16(rawCol<<2)>>2)
		if col < 0 {
			col += 256
		} else if col >= 256 {
			col -= 256
		}
	}
	if row < 0 || row >= 65536 || col < 0 || col >= 256 {
		return "", false
	}
	var b strings.Builder
	if rawCol&0x4000 == 0 {
		b.WriteByte('$')
	}
	b.WriteString(ColumnName(col + 1))
	if rawCol&0x8000 == 0 {
		b.WriteByte('$')
	}
	b.WriteString(strconv.Itoa(row + 1))
	return b.String(), true
}

var xlsFunctionNames = map[uint16]string{
	0: "COUNT", 1: "IF", 4: "SUM", 5: "AVERAGE", 6: "MIN", 7: "MAX", 8: "ROW", 9: "COLUMN",
	10: "NA", 15: "SIN", 16: "COS", 17: "TAN", 19: "PI", 20: "SQRT", 21: "EXP", 22: "LN", 23: "LOG10", 24: "ABS",
	25: "INT", 27: "ROUND", 29: "INDEX", 31: "MID", 32: "LEN", 34: "TRUE", 35: "FALSE", 36: "AND", 37: "OR", 38: "NOT",
	39: "MOD", 48: "TEXT", 63: "RAND", 64: "MATCH", 65: "DATE", 66: "TIME", 74: "NOW", 82: "SEARCH", 97: "ATAN2",
	100: "CHOOSE", 101: "HLOOKUP", 102: "VLOOKUP", 109: "LOG", 115: "LEFT", 116: "RIGHT", 117: "EXACT", 124: "FIND",
	125: "CELL", 148: "INDIRECT", 169: "COUNTA", 183: "PRODUCT", 197: "TRUNC", 198: "ISLOGICAL", 210: "ROUNDUP",
	211: "ROUNDDOWN", 228: "SUMPRODUCT", 279: "EVEN", 280: "ODD", 321: "RANDBETWEEN", 345: "SUMIF", 346: "COUNTIF", 357: "SUMIFS", 358: "COUNTIFS",
}

var xlsFixedArity = map[uint16]int{10: 0, 15: 1, 16: 1, 17: 1, 19: 0, 20: 1, 21: 1, 22: 1, 23: 1, 24: 1, 25: 1, 27: 2, 32: 1, 34: 0, 35: 0, 38: 1, 39: 2, 48: 2, 63: 0, 65: 3, 66: 3, 74: 0, 97: 2, 117: 2, 198: 1, 279: 1, 280: 1, 321: 2}
