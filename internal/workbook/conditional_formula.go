package workbook

import (
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"excellens/internal/formats"
)

type conditionalDecimal struct {
	digits   string
	exponent int64
	negative bool
}

type conditionalValue struct {
	kind         string
	text, folded string
	number       conditionalDecimal
	rat          *big.Rat
	boolean      bool
}

type conditionalReference struct {
	row, col                 int
	absoluteRow, absoluteCol bool
}

type conditionalExprKind uint8

const (
	cfExprLiteral conditionalExprKind = iota + 1
	cfExprRef
	cfExprRange
	cfExprUnary
	cfExprPercent
	cfExprBinary
	cfExprCall
)

type conditionalExpr struct {
	kind   conditionalExprKind
	op     string
	value  conditionalValue
	ref    conditionalReference
	endRef conditionalReference
	args   []*conditionalExpr
}

func parseConditionalDecimal(raw string) (conditionalDecimal, bool) {
	key, ok := inspectNumericKey(raw)
	if !ok {
		return conditionalDecimal{}, false
	}
	if key == "0" {
		return conditionalDecimal{digits: "0"}, true
	}
	out := conditionalDecimal{}
	if key[0] == '-' {
		out.negative = true
		key = key[1:]
	}
	at := strings.LastIndexByte(key, 'e')
	if at < 0 {
		return conditionalDecimal{}, false
	}
	out.digits = key[:at]
	out.exponent, _ = strconv.ParseInt(key[at+1:], 10, 64)
	return out, true
}

// Decimal digits and a bounded exponent compare without float rounding or
// constructing enormous powers of ten for a hostile scientific literal.
func compareConditionalDecimal(a, b conditionalDecimal) int {
	az, bz := a.digits == "0", b.digits == "0"
	if az && bz {
		return 0
	}
	if a.negative != b.negative {
		if a.negative {
			return -1
		}
		return 1
	}
	cmp := 0
	if az {
		cmp = -1
	} else if bz {
		cmp = 1
	} else {
		am, bm := int64(len(a.digits))+a.exponent, int64(len(b.digits))+b.exponent
		if am < bm {
			cmp = -1
		} else if am > bm {
			cmp = 1
		} else {
			for i := 0; i < max(len(a.digits), len(b.digits)); i++ {
				x, y := byte('0'), byte('0')
				if i < len(a.digits) {
					x = a.digits[i]
				}
				if i < len(b.digits) {
					y = b.digits[i]
				}
				if x < y {
					cmp = -1
					break
				}
				if x > y {
					cmp = 1
					break
				}
			}
		}
	}
	if a.negative {
		return -cmp
	}
	return cmp
}

func conditionalText(value string) conditionalValue {
	return conditionalValue{kind: "text", text: value, folded: filterFoldKey(value)}
}

func conditionalNumberVal(d conditionalDecimal) conditionalValue {
	return conditionalValue{kind: "number", number: d}
}

func conditionalIntVal(n int64) conditionalValue {
	d, _ := parseConditionalDecimal(strconv.FormatInt(n, 10))
	return conditionalValue{kind: "number", number: d}
}

func conditionalBoolVal(b bool) conditionalValue {
	return conditionalValue{kind: "boolean", boolean: b}
}

func conditionalErrorVal(code string) conditionalValue {
	return conditionalValue{kind: "error", text: code}
}

func conditionalBlankVal() conditionalValue {
	return conditionalValue{kind: "blank"}
}

func parseConditionalCellRef(raw string) (conditionalReference, bool) {
	s := strings.TrimSpace(raw)
	if len(s) < 2 || len(s) > 12 {
		return conditionalReference{}, false
	}
	i := 0
	absoluteCol := s[0] == '$'
	if absoluteCol {
		i++
	}
	letters := i
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		i++
	}
	if i == letters || i-letters > 3 {
		return conditionalReference{}, false
	}
	absoluteRow := i < len(s) && s[i] == '$'
	if absoluteRow {
		i++
	}
	digits := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i != len(s) || digits == i {
		return conditionalReference{}, false
	}
	col, row, err := ParseCellRef(s)
	if err != nil || row < 1 || row > formats.ExcelRows || col < 1 || col > formats.ExcelColumns {
		return conditionalReference{}, false
	}
	return conditionalReference{row: row, col: col, absoluteCol: absoluteCol, absoluteRow: absoluteRow}, true
}

func parseConditionalColRef(raw string) (conditionalReference, bool) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || len(s) > 4 {
		return conditionalReference{}, false
	}
	i := 0
	absoluteCol := s[0] == '$'
	if absoluteCol {
		i++
	}
	letters := i
	col := 0
	for i < len(s) && (s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z') {
		ch := s[i]
		if ch >= 'a' {
			ch -= 32
		}
		col = col*26 + int(ch-'A'+1)
		i++
	}
	if i != len(s) || i == letters || i-letters > 3 || col < 1 || col > formats.ExcelColumns {
		return conditionalReference{}, false
	}
	return conditionalReference{col: col, absoluteCol: absoluteCol, absoluteRow: true}, true
}

func parseConditionalRowRef(raw string) (conditionalReference, bool) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || len(s) > 8 {
		return conditionalReference{}, false
	}
	i := 0
	absoluteRow := s[0] == '$'
	if absoluteRow {
		i++
	}
	digits := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i != len(s) || i == digits {
		return conditionalReference{}, false
	}
	row, err := strconv.Atoi(s[digits:])
	if err != nil || row < 1 || row > formats.ExcelRows {
		return conditionalReference{}, false
	}
	return conditionalReference{row: row, absoluteRow: absoluteRow, absoluteCol: true}, true
}

type cfFuncSpec struct {
	minArgs, maxArgs int
}

var conditionalFunctions = map[string]cfFuncSpec{
	"TRUE":        {0, 0},
	"FALSE":       {0, 0},
	"AND":         {1, 255},
	"OR":          {1, 255},
	"XOR":         {1, 255},
	"NOT":         {1, 1},
	"IF":          {2, 3},
	"IFS":         {2, 254},
	"IFERROR":     {2, 2},
	"IFNA":        {2, 2},
	"ISBLANK":     {1, 1},
	"ISNUMBER":    {1, 1},
	"ISTEXT":      {1, 1},
	"ISNONTEXT":   {1, 1},
	"ISLOGICAL":   {1, 1},
	"ISERROR":     {1, 1},
	"ISERR":       {1, 1},
	"ISNA":        {1, 1},
	"ISEVEN":      {1, 1},
	"ISODD":       {1, 1},
	"N":           {1, 1},
	"T":           {1, 1},
	"NA":          {0, 0},
	"ROW":         {0, 1},
	"COLUMN":      {0, 1},
	"ROWS":        {1, 1},
	"COLUMNS":     {1, 1},
	"ABS":         {1, 1},
	"SIGN":        {1, 1},
	"INT":         {1, 1},
	"TRUNC":       {1, 2},
	"ROUND":       {2, 2},
	"ROUNDUP":     {2, 2},
	"ROUNDDOWN":   {2, 2},
	"MOD":         {2, 2},
	"QUOTIENT":    {2, 2},
	"POWER":       {2, 2},
	"SQRT":        {1, 1},
	"SUM":         {1, 255},
	"AVERAGE":     {1, 255},
	"MIN":         {1, 255},
	"MAX":         {1, 255},
	"COUNT":       {1, 255},
	"COUNTA":      {1, 255},
	"COUNTBLANK":  {1, 1},
	"PRODUCT":     {1, 255},
	"LEN":         {1, 1},
	"TRIM":        {1, 1},
	"CLEAN":       {1, 1},
	"LEFT":        {1, 2},
	"RIGHT":       {1, 2},
	"MID":         {3, 3},
	"UPPER":       {1, 1},
	"LOWER":       {1, 1},
	"EXACT":       {2, 2},
	"FIND":        {2, 3},
	"SEARCH":      {2, 3},
	"SUBSTITUTE":  {3, 4},
	"REPLACE":     {4, 4},
	"REPT":        {2, 2},
	"CONCAT":      {1, 255},
	"CONCATENATE": {1, 255},
	"VALUE":       {1, 1},
	"COUNTIF":     {2, 2},
	"COUNTIFS":    {2, 254},
	"SUMIF":       {2, 3},
	"SUMIFS":      {3, 255},
	"MATCH":       {2, 3},
	"DATE":        {3, 3},
	"YEAR":        {1, 1},
	"MONTH":       {1, 1},
	"DAY":         {1, 1},
	"WEEKDAY":     {1, 2},
}

type cfParser struct {
	text      string
	sheetName string
	pos       int
	depth     int
	nodes     int
	notice    string
}

func compileConditionalExpr(raw, sheetName string) (*conditionalExpr, int, string) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || len(s) > formats.ConditionalFormulaBytes {
		return nil, 0, "条件格式公式尚未求值"
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "="))
	if s == "" {
		return nil, 0, "条件格式公式尚未求值"
	}
	p := cfParser{text: s, sheetName: sheetName}
	expr, ok := p.parseComparison()
	p.skipSpace()
	if !ok || p.pos != len(p.text) {
		if p.notice != "" {
			return nil, 0, p.notice
		}
		return nil, 0, "条件格式公式尚未求值"
	}
	return expr, p.nodes, ""
}

func (p *cfParser) skipSpace() {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *cfParser) alloc(node conditionalExpr) (*conditionalExpr, bool) {
	p.nodes++
	if p.nodes > formats.ConditionalExprNodes {
		p.notice = "条件格式公式过于复杂，尚未求值"
		return nil, false
	}
	return &node, true
}

func (p *cfParser) enter() bool {
	p.depth++
	if p.depth > formats.ConditionalExprDepth {
		p.notice = "条件格式公式嵌套过深，尚未求值"
		return false
	}
	return true
}

func (p *cfParser) leave() { p.depth-- }

func (p *cfParser) parseComparison() (*conditionalExpr, bool) {
	left, ok := p.parseConcat()
	if !ok {
		return nil, false
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.text) {
			break
		}
		op := ""
		rest := p.text[p.pos:]
		switch {
		case strings.HasPrefix(rest, "<="):
			op = "<="
		case strings.HasPrefix(rest, ">="):
			op = ">="
		case strings.HasPrefix(rest, "<>"):
			op = "<>"
		case rest[0] == '=', rest[0] == '<', rest[0] == '>':
			op = rest[:1]
		}
		if op == "" {
			break
		}
		p.pos += len(op)
		right, ok := p.parseConcat()
		if !ok {
			return nil, false
		}
		left, ok = p.alloc(conditionalExpr{kind: cfExprBinary, op: op, args: []*conditionalExpr{left, right}})
		if !ok {
			return nil, false
		}
	}
	return left, true
}

func (p *cfParser) parseConcat() (*conditionalExpr, bool) {
	left, ok := p.parseAdditive()
	if !ok {
		return nil, false
	}
	for {
		p.skipSpace()
		if p.pos < len(p.text) && p.text[p.pos] == '&' {
			p.pos++
			right, ok := p.parseAdditive()
			if !ok {
				return nil, false
			}
			left, ok = p.alloc(conditionalExpr{kind: cfExprBinary, op: "&", args: []*conditionalExpr{left, right}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return left, true
}

func (p *cfParser) parseAdditive() (*conditionalExpr, bool) {
	left, ok := p.parseMultiplicative()
	if !ok {
		return nil, false
	}
	for {
		p.skipSpace()
		if p.pos < len(p.text) && (p.text[p.pos] == '+' || p.text[p.pos] == '-') {
			op := p.text[p.pos : p.pos+1]
			p.pos++
			right, ok := p.parseMultiplicative()
			if !ok {
				return nil, false
			}
			left, ok = p.alloc(conditionalExpr{kind: cfExprBinary, op: op, args: []*conditionalExpr{left, right}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return left, true
}

func (p *cfParser) parseMultiplicative() (*conditionalExpr, bool) {
	left, ok := p.parsePower()
	if !ok {
		return nil, false
	}
	for {
		p.skipSpace()
		if p.pos < len(p.text) && (p.text[p.pos] == '*' || p.text[p.pos] == '/') {
			op := p.text[p.pos : p.pos+1]
			p.pos++
			right, ok := p.parsePower()
			if !ok {
				return nil, false
			}
			left, ok = p.alloc(conditionalExpr{kind: cfExprBinary, op: op, args: []*conditionalExpr{left, right}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return left, true
}

func (p *cfParser) parsePower() (*conditionalExpr, bool) {
	left, ok := p.parsePercent()
	if !ok {
		return nil, false
	}
	for {
		p.skipSpace()
		if p.pos < len(p.text) && p.text[p.pos] == '^' {
			p.pos++
			right, ok := p.parsePercent()
			if !ok {
				return nil, false
			}
			left, ok = p.alloc(conditionalExpr{kind: cfExprBinary, op: "^", args: []*conditionalExpr{left, right}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return left, true
}

func (p *cfParser) parsePercent() (*conditionalExpr, bool) {
	node, ok := p.parseUnary()
	if !ok {
		return nil, false
	}
	for {
		p.skipSpace()
		if p.pos < len(p.text) && p.text[p.pos] == '%' {
			p.pos++
			if node.kind == cfExprLiteral && node.value.kind == "number" {
				node.value.number.exponent -= 2
				continue
			}
			node, ok = p.alloc(conditionalExpr{kind: cfExprPercent, args: []*conditionalExpr{node}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return node, true
}

func (p *cfParser) parseUnary() (*conditionalExpr, bool) {
	p.skipSpace()
	if p.pos < len(p.text) && (p.text[p.pos] == '+' || p.text[p.pos] == '-') {
		if !p.enter() {
			return nil, false
		}
		defer p.leave()
		op := p.text[p.pos : p.pos+1]
		p.pos++
		operand, ok := p.parseUnary()
		if !ok {
			return nil, false
		}
		if operand.kind == cfExprLiteral && operand.value.kind == "number" {
			if op == "-" && operand.value.number.digits != "0" {
				operand.value.number.negative = !operand.value.number.negative
			}
			return operand, true
		}
		return p.alloc(conditionalExpr{kind: cfExprUnary, op: op, args: []*conditionalExpr{operand}})
	}
	return p.parsePrimary()
}

func (p *cfParser) parsePrimary() (*conditionalExpr, bool) {
	p.skipSpace()
	if p.pos >= len(p.text) {
		return nil, false
	}
	ch := p.text[p.pos]
	switch {
	case ch == '(':
		if !p.enter() {
			return nil, false
		}
		defer p.leave()
		p.pos++
		inner, ok := p.parseComparison()
		if !ok {
			return nil, false
		}
		p.skipSpace()
		if p.pos >= len(p.text) || p.text[p.pos] != ')' {
			return nil, false
		}
		p.pos++
		return inner, true
	case ch == '"':
		return p.parseString()
	case ch == '#':
		return p.parseErrorLiteral()
	case ch == '\'':
		return p.parseQuotedSheetRef()
	case ch == '$':
		return p.parseRefOrRange()
	case ch >= '0' && ch <= '9' || ch == '.':
		// Whole-row range like 2:5 vs numeric literal.
		if ch >= '0' && ch <= '9' {
			if expr, ok := p.tryRowRange(); ok {
				return expr, true
			}
		}
		return p.parseNumber()
	default:
		return p.parseIdentOrRef()
	}
}

func (p *cfParser) parseString() (*conditionalExpr, bool) {
	p.pos++ // skip opening '"'
	var b strings.Builder
	for p.pos < len(p.text) {
		ch := p.text[p.pos]
		p.pos++
		if ch == '"' {
			if p.pos < len(p.text) && p.text[p.pos] == '"' {
				b.WriteByte('"')
				p.pos++
				continue
			}
			return p.alloc(conditionalExpr{kind: cfExprLiteral, value: conditionalText(b.String())})
		}
		b.WriteByte(ch)
	}
	return nil, false
}

func (p *cfParser) parseErrorLiteral() (*conditionalExpr, bool) {
	rest := p.text[p.pos:]
	for _, code := range []string{"#VALUE!", "#DIV/0!", "#NAME?", "#NULL!", "#REF!", "#NUM!", "#N/A"} {
		if len(rest) >= len(code) && strings.EqualFold(rest[:len(code)], code) {
			p.pos += len(code)
			return p.alloc(conditionalExpr{kind: cfExprLiteral, value: conditionalErrorVal(code)})
		}
	}
	return nil, false
}

func (p *cfParser) parseNumber() (*conditionalExpr, bool) {
	start := p.pos
	seenDigit, seenDot := false, false
	for p.pos < len(p.text) {
		ch := p.text[p.pos]
		if ch >= '0' && ch <= '9' {
			seenDigit = true
			p.pos++
		} else if ch == '.' && !seenDot {
			seenDot = true
			p.pos++
		} else {
			break
		}
	}
	if !seenDigit {
		return nil, false
	}
	if p.pos < len(p.text) && (p.text[p.pos] == 'e' || p.text[p.pos] == 'E') {
		save := p.pos
		p.pos++
		if p.pos < len(p.text) && (p.text[p.pos] == '+' || p.text[p.pos] == '-') {
			p.pos++
		}
		expStart := p.pos
		for p.pos < len(p.text) && p.text[p.pos] >= '0' && p.text[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == expStart {
			p.pos = save
		}
	}
	d, ok := parseConditionalDecimal(p.text[start:p.pos])
	if !ok {
		return nil, false
	}
	return p.alloc(conditionalExpr{kind: cfExprLiteral, value: conditionalNumberVal(d)})
}

func (p *cfParser) tryRowRange() (*conditionalExpr, bool) {
	save := p.pos
	for p.pos < len(p.text) && p.text[p.pos] >= '0' && p.text[p.pos] <= '9' {
		p.pos++
	}
	if p.pos >= len(p.text) || p.text[p.pos] != ':' {
		p.pos = save
		return nil, false
	}
	first, ok := parseConditionalRowRef(p.text[save:p.pos])
	if !ok {
		p.pos = save
		return nil, false
	}
	p.pos++ // skip ':'
	endStart := p.pos
	if p.pos < len(p.text) && p.text[p.pos] == '$' {
		p.pos++
	}
	for p.pos < len(p.text) && p.text[p.pos] >= '0' && p.text[p.pos] <= '9' {
		p.pos++
	}
	second, ok := parseConditionalRowRef(p.text[endStart:p.pos])
	if !ok {
		p.pos = save
		return nil, false
	}
	return p.alloc(conditionalExpr{kind: cfExprRange, ref: first, endRef: second})
}

func (p *cfParser) parseQuotedSheetRef() (*conditionalExpr, bool) {
	p.pos++ // skip opening '\''
	var name strings.Builder
	closed := false
	for p.pos < len(p.text) {
		ch := p.text[p.pos]
		p.pos++
		if ch == '\'' {
			if p.pos < len(p.text) && p.text[p.pos] == '\'' {
				name.WriteByte('\'')
				p.pos++
				continue
			}
			closed = true
			break
		}
		name.WriteByte(ch)
	}
	if !closed || p.pos >= len(p.text) || p.text[p.pos] != '!' {
		return nil, false
	}
	p.pos++ // skip '!'
	sheet := name.String()
	if p.sheetName == "" || strings.ContainsAny(sheet, "[]") || !strings.EqualFold(sheet, p.sheetName) {
		p.notice = "跨工作表条件格式引用尚未求值"
		return nil, false
	}
	return p.parseRefOrRange()
}

func (p *cfParser) parseIdentOrRef() (*conditionalExpr, bool) {
	start := p.pos
	// Check for unquoted sheet name followed by '!' (may include Unicode letters/digits/_/.).
	for p.pos < len(p.text) {
		r, size := decodeUTF8At(p.text, p.pos)
		if r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			p.pos += size
			continue
		}
		break
	}
	if p.pos == start {
		return nil, false
	}
	token := p.text[start:p.pos]
	// Unquoted sheet-qualified reference: Sheet1!A1
	if p.pos < len(p.text) && p.text[p.pos] == '!' {
		p.pos++
		if p.sheetName == "" || !strings.EqualFold(token, p.sheetName) {
			p.notice = "跨工作表条件格式引用尚未求值"
			return nil, false
		}
		return p.parseRefOrRange()
	}
	// Function call: FUNC(...)
	p.skipSpace()
	if p.pos < len(p.text) && p.text[p.pos] == '(' {
		return p.parseCall(token)
	}
	// Boolean literal (not followed by ':' or '$')
	if p.pos >= len(p.text) || (p.text[p.pos] != ':' && p.text[p.pos] != '$') {
		switch strings.ToUpper(token) {
		case "TRUE":
			return p.alloc(conditionalExpr{kind: cfExprLiteral, value: conditionalBoolVal(true)})
		case "FALSE":
			return p.alloc(conditionalExpr{kind: cfExprLiteral, value: conditionalBoolVal(false)})
		}
	}
	// Rewind and parse as cell ref / range ref (handles A$1, A:C, A1:B5)
	p.pos = start
	return p.parseRefOrRange()
}

func (p *cfParser) parseCall(rawName string) (*conditionalExpr, bool) {
	if !p.enter() {
		return nil, false
	}
	defer p.leave()
	name := strings.ToUpper(rawName)
	name = strings.TrimPrefix(name, "_XLFN.")
	spec, known := conditionalFunctions[name]
	if !known {
		p.notice = "条件格式公式尚未求值"
		return nil, false
	}
	p.pos++ // skip '('
	p.skipSpace()
	var args []*conditionalExpr
	if p.pos < len(p.text) && p.text[p.pos] == ')' {
		p.pos++
	} else {
		for {
			p.skipSpace()
			var arg *conditionalExpr
			var ok bool
			if p.pos < len(p.text) && (p.text[p.pos] == ',' || p.text[p.pos] == ')') {
				arg, ok = p.alloc(conditionalExpr{kind: cfExprLiteral, value: conditionalBlankVal()})
			} else {
				arg, ok = p.parseComparison()
			}
			if !ok {
				return nil, false
			}
			args = append(args, arg)
			if len(args) > spec.maxArgs {
				return nil, false
			}
			p.skipSpace()
			if p.pos < len(p.text) && p.text[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.pos < len(p.text) && p.text[p.pos] == ')' {
				p.pos++
				break
			}
			return nil, false
		}
	}
	if len(args) < spec.minArgs || len(args) > spec.maxArgs {
		return nil, false
	}
	if (name == "IFS" || name == "COUNTIFS") && len(args)%2 != 0 {
		return nil, false
	}
	if name == "SUMIFS" && len(args)%2 == 0 {
		return nil, false
	}
	return p.alloc(conditionalExpr{kind: cfExprCall, op: name, args: args})
}

func (p *cfParser) scanRefToken() string {
	start := p.pos
	for p.pos < len(p.text) {
		ch := p.text[p.pos]
		if ch == '$' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' {
			p.pos++
			continue
		}
		break
	}
	return p.text[start:p.pos]
}

func (p *cfParser) parseRefOrRange() (*conditionalExpr, bool) {
	firstTok := p.scanRefToken()
	if firstTok == "" {
		return nil, false
	}
	if p.pos < len(p.text) && p.text[p.pos] == ':' {
		p.pos++ // skip ':'
		secondTok := p.scanRefToken()
		if secondTok == "" {
			return nil, false
		}
		if a, ok1 := parseConditionalCellRef(firstTok); ok1 {
			if b, ok2 := parseConditionalCellRef(secondTok); ok2 {
				return p.alloc(conditionalExpr{kind: cfExprRange, ref: a, endRef: b})
			}
			return nil, false
		}
		if a, ok1 := parseConditionalColRef(firstTok); ok1 {
			if b, ok2 := parseConditionalColRef(secondTok); ok2 {
				return p.alloc(conditionalExpr{kind: cfExprRange, ref: a, endRef: b})
			}
			return nil, false
		}
		if a, ok1 := parseConditionalRowRef(firstTok); ok1 {
			if b, ok2 := parseConditionalRowRef(secondTok); ok2 {
				return p.alloc(conditionalExpr{kind: cfExprRange, ref: a, endRef: b})
			}
			return nil, false
		}
		return nil, false
	}
	ref, ok := parseConditionalCellRef(firstTok)
	if !ok {
		return nil, false
	}
	return p.alloc(conditionalExpr{kind: cfExprRef, ref: ref})
}

func decodeUTF8At(s string, i int) (rune, int) {
	if s[i] < 0x80 {
		return rune(s[i]), 1
	}
	return utf8.DecodeRuneInString(s[i:])
}

func compareConditionalValues(a, b conditionalValue) (int, bool) {
	if a.kind == "error" || b.kind == "error" {
		return 0, false
	}
	if a.kind == "blank" {
		a = conditionalBlankAs(b.kind)
	}
	if b.kind == "blank" {
		b = conditionalBlankAs(a.kind)
	}
	rank := func(kind string) int {
		switch kind {
		case "number":
			return 0
		case "text":
			return 1
		case "boolean":
			return 2
		}
		return -1
	}
	if a.kind != b.kind {
		return rank(a.kind) - rank(b.kind), true
	}
	switch a.kind {
	case "number":
		if a.rat != nil || b.rat != nil {
			return compareConditionalNumbersExact(a, b), true
		}
		return compareConditionalDecimal(a.number, b.number), true
	case "text":
		return strings.Compare(a.folded, b.folded), true
	case "boolean":
		if a.boolean == b.boolean {
			return 0, true
		}
		if a.boolean {
			return 1, true
		}
		return -1, true
	}
	return 0, false
}

func conditionalBlankAs(kind string) conditionalValue {
	switch kind {
	case "text":
		return conditionalText("")
	case "boolean":
		return conditionalValue{kind: "boolean"}
	}
	return conditionalValue{kind: "number", number: conditionalDecimal{digits: "0"}}
}

func matchConditionalComparison(cmp int, operator string) bool {
	switch operator {
	case "=", "equal":
		return cmp == 0
	case "<>", "notEqual":
		return cmp != 0
	case ">", "greaterThan":
		return cmp > 0
	case "<", "lessThan":
		return cmp < 0
	case ">=", "greaterThanOrEqual":
		return cmp >= 0
	case "<=", "lessThanOrEqual":
		return cmp <= 0
	}
	return false
}
