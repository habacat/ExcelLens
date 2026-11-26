package conditional

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"excellens/internal/formats"
)

type Kind uint8

const (
	Literal Kind = iota + 1
	ReferenceCell
	Range
	Unary
	Percent
	Binary
	Call
)

// Expression holds typed literals, source references and operators. Workbook
// bindings supply the literal representation and scoped name lookup at compile time.
type Expression[V any] struct {
	Kind        Kind
	Op          string
	Value       V
	Ref, EndRef Reference
	Args        []*Expression[V]
}
type Bindings[V any] struct {
	Text   func(string) V
	Number func(string) (V, bool)
	Bool   func(bool) V
	Error  func(string) V
	Blank  V
	Fold   func(*V, string) bool
	Name   func(string) (Reference, string)
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

type parser[V any] struct {
	bindings  Bindings[V]
	text      string
	sheetName string
	pos       int
	depth     int
	nodes     int
	notice    string
}

func Compile[V any](raw, sheetName string, bindings Bindings[V]) (*Expression[V], int, string) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 || len(s) > formats.ConditionalFormulaBytes {
		return nil, 0, "条件格式公式尚未求值"
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, "="))
	if s == "" {
		return nil, 0, "条件格式公式尚未求值"
	}
	p := parser[V]{text: s, sheetName: sheetName, bindings: bindings}
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

func (p *parser[V]) skipSpace() {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser[V]) alloc(node Expression[V]) (*Expression[V], bool) {
	p.nodes++
	if p.nodes > formats.ConditionalExprNodes {
		p.notice = "条件格式公式过于复杂，尚未求值"
		return nil, false
	}
	return &node, true
}

func (p *parser[V]) enter() bool {
	p.depth++
	if p.depth > formats.ConditionalExprDepth {
		p.notice = "条件格式公式嵌套过深，尚未求值"
		return false
	}
	return true
}

func (p *parser[V]) leave() { p.depth-- }

func (p *parser[V]) parseComparison() (*Expression[V], bool) {
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
		left, ok = p.alloc(Expression[V]{Kind: Binary, Op: op, Args: []*Expression[V]{left, right}})
		if !ok {
			return nil, false
		}
	}
	return left, true
}

func (p *parser[V]) parseConcat() (*Expression[V], bool) {
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
			left, ok = p.alloc(Expression[V]{Kind: Binary, Op: "&", Args: []*Expression[V]{left, right}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return left, true
}

func (p *parser[V]) parseAdditive() (*Expression[V], bool) {
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
			left, ok = p.alloc(Expression[V]{Kind: Binary, Op: op, Args: []*Expression[V]{left, right}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return left, true
}

func (p *parser[V]) parseMultiplicative() (*Expression[V], bool) {
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
			left, ok = p.alloc(Expression[V]{Kind: Binary, Op: op, Args: []*Expression[V]{left, right}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return left, true
}

func (p *parser[V]) parsePower() (*Expression[V], bool) {
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
			left, ok = p.alloc(Expression[V]{Kind: Binary, Op: "^", Args: []*Expression[V]{left, right}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return left, true
}

func (p *parser[V]) parsePercent() (*Expression[V], bool) {
	node, ok := p.parseUnary()
	if !ok {
		return nil, false
	}
	for {
		p.skipSpace()
		if p.pos < len(p.text) && p.text[p.pos] == '%' {
			p.pos++
			if node.Kind == Literal && p.bindings.Fold(&node.Value, "%") {
				continue
			}
			node, ok = p.alloc(Expression[V]{Kind: Percent, Args: []*Expression[V]{node}})
			if !ok {
				return nil, false
			}
			continue
		}
		break
	}
	return node, true
}

func (p *parser[V]) parseUnary() (*Expression[V], bool) {
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
		if operand.Kind == Literal && p.bindings.Fold(&operand.Value, op) {
			return operand, true
		}
		return p.alloc(Expression[V]{Kind: Unary, Op: op, Args: []*Expression[V]{operand}})
	}
	return p.parsePrimary()
}

func (p *parser[V]) parsePrimary() (*Expression[V], bool) {
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

func (p *parser[V]) parseString() (*Expression[V], bool) {
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
			return p.alloc(Expression[V]{Kind: Literal, Value: p.bindings.Text(b.String())})
		}
		b.WriteByte(ch)
	}
	return nil, false
}

func (p *parser[V]) parseErrorLiteral() (*Expression[V], bool) {
	rest := p.text[p.pos:]
	for _, code := range []string{"#VALUE!", "#DIV/0!", "#NAME?", "#NULL!", "#REF!", "#NUM!", "#N/A"} {
		if len(rest) >= len(code) && strings.EqualFold(rest[:len(code)], code) {
			p.pos += len(code)
			return p.alloc(Expression[V]{Kind: Literal, Value: p.bindings.Error(code)})
		}
	}
	return nil, false
}

func (p *parser[V]) parseNumber() (*Expression[V], bool) {
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
	d, ok := p.bindings.Number(p.text[start:p.pos])
	if !ok {
		return nil, false
	}
	return p.alloc(Expression[V]{Kind: Literal, Value: d})
}

func (p *parser[V]) tryRowRange() (*Expression[V], bool) {
	save := p.pos
	for p.pos < len(p.text) && p.text[p.pos] >= '0' && p.text[p.pos] <= '9' {
		p.pos++
	}
	if p.pos >= len(p.text) || p.text[p.pos] != ':' {
		p.pos = save
		return nil, false
	}
	first, ok := ParseRowRef(p.text[save:p.pos])
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
	second, ok := ParseRowRef(p.text[endStart:p.pos])
	if !ok {
		p.pos = save
		return nil, false
	}
	return p.alloc(Expression[V]{Kind: Range, Ref: first, EndRef: second})
}

func (p *parser[V]) scanIdentifier() {
	for p.pos < len(p.text) {
		r, size := decodeUTF8At(p.text, p.pos)
		if r == '_' || r == '.' || r == '\\' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			p.pos += size
			continue
		}
		break
	}
}

// BIFF12 qualifies worksheet-local names even on the current sheet.
func (p *parser[V]) parseQualifiedOperand() (*Expression[V], bool) {
	start := p.pos
	if n, ok := p.parseRefOrRange(); ok {
		return n, true
	}
	p.pos = start
	p.scanIdentifier()
	if p.pos == start {
		return nil, false
	}
	return p.parseDefinedName(p.text[start:p.pos])
}

func (p *parser[V]) parseQuotedSheetRef() (*Expression[V], bool) {
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
	return p.parseQualifiedOperand()
}

func (p *parser[V]) parseIdentOrRef() (*Expression[V], bool) {
	start := p.pos
	// Check for unquoted sheet name followed by '!' (may include Unicode letters/digits/_/.).
	p.scanIdentifier()
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
		return p.parseQualifiedOperand()
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
			return p.alloc(Expression[V]{Kind: Literal, Value: p.bindings.Bool(true)})
		case "FALSE":
			return p.alloc(Expression[V]{Kind: Literal, Value: p.bindings.Bool(false)})
		}
	}
	if p.pos == len(p.text) || (p.text[p.pos] != ':' && p.text[p.pos] != '$') {
		if _, isCell := ParseCellRef(token); !isCell {
			return p.parseDefinedName(token)
		}
	}
	// Rewind and parse as cell ref / range ref (handles A$1, A:C, A1:B5)
	p.pos = start
	return p.parseRefOrRange()
}

func (p *parser[V]) parseCall(rawName string) (*Expression[V], bool) {
	if !p.enter() {
		return nil, false
	}
	defer p.leave()
	name := strings.ToUpper(rawName)
	name = strings.TrimPrefix(name, "_XLFN.")
	spec, known := conditionalFunctions[name]
	if !known {
		p.notice = "条件格式暂不支持函数 " + name
		return nil, false
	}
	p.pos++ // skip '('
	p.skipSpace()
	var args []*Expression[V]
	if p.pos < len(p.text) && p.text[p.pos] == ')' {
		p.pos++
	} else {
		for {
			p.skipSpace()
			var arg *Expression[V]
			var ok bool
			if p.pos < len(p.text) && (p.text[p.pos] == ',' || p.text[p.pos] == ')') {
				arg, ok = p.alloc(Expression[V]{Kind: Literal, Value: p.bindings.Blank})
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
	return p.alloc(Expression[V]{Kind: Call, Op: name, Args: args})
}

func (p *parser[V]) scanRefToken() string {
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

func (p *parser[V]) parseRefOrRange() (*Expression[V], bool) {
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
		if a, ok1 := ParseCellRef(firstTok); ok1 {
			if b, ok2 := ParseCellRef(secondTok); ok2 {
				return p.alloc(Expression[V]{Kind: Range, Ref: a, EndRef: b})
			}
			return nil, false
		}
		if a, ok1 := ParseColumnRef(firstTok); ok1 {
			if b, ok2 := ParseColumnRef(secondTok); ok2 {
				return p.alloc(Expression[V]{Kind: Range, Ref: a, EndRef: b})
			}
			return nil, false
		}
		if a, ok1 := ParseRowRef(firstTok); ok1 {
			if b, ok2 := ParseRowRef(secondTok); ok2 {
				return p.alloc(Expression[V]{Kind: Range, Ref: a, EndRef: b})
			}
			return nil, false
		}
		return nil, false
	}
	ref, ok := ParseCellRef(firstTok)
	if !ok {
		return nil, false
	}
	return p.alloc(Expression[V]{Kind: ReferenceCell, Ref: ref})
}

func decodeUTF8At(s string, i int) (rune, int) {
	if s[i] < 0x80 {
		return rune(s[i]), 1
	}
	return utf8.DecodeRuneInString(s[i:])
}

func (p *parser[V]) parseDefinedName(name string) (*Expression[V], bool) {
	ref, notice := p.bindings.Name(name)
	if notice != "" {
		p.notice = notice
		return nil, false
	}
	return p.alloc(Expression[V]{Kind: ReferenceCell, Ref: ref})
}
