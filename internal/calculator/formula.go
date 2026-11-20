package calculator

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxFormulaText = 64 << 10

type FormulaValue struct {
	Kind   string
	Number *big.Rat
	Text   string
	Bool   bool
	Range  *RangeValue
	// NumericText marks imported accounting amounts while keeping text identity.
	NumericText *big.Rat
}

func NumberValue(v *big.Rat) FormulaValue { return FormulaValue{Kind: "number", Number: v} }
func TextValue(s string) FormulaValue     { return FormulaValue{Kind: "text", Text: s} }
func BoolValue(b bool) FormulaValue       { return FormulaValue{Kind: "boolean", Bool: b} }
func BlankValue() FormulaValue            { return FormulaValue{Kind: "blank"} }

type RangeValue struct {
	Aggregate                      func() (RangeValue, error)
	Sum, Min, Max                  *big.Rat
	Count, NonEmpty                int64
	Text                           bool // compatibility with numeric-only resolver adapters
	Scalar                         *FormulaValue
	Fault                          error
	Load                           func() ([]FormulaValue, error)
	Rows, Cols, FirstRow, FirstCol int
	Visit                          func(func(int, FormulaValue) error) error
	At                             func(int) (FormulaValue, error)
	// Slice lets a sparse worksheet resolve only the lookup vector or return
	// column, without evaluating unrelated formulas in the rectangular table.
	Slice         func(row, col, rows, cols int) RangeValue
	VisitSubtotal func(skipHidden bool, visit func(int, FormulaValue) error) error
	// Ordered promises increasing source positions from Visit.
	Ordered bool
	// Spill resolves the current dynamic array owned by a single reference.
	Spill func() (RangeValue, error)
}
type FormulaResolver func(sheet, start, end string) (RangeValue, error)
type formulaNode struct {
	rows, cols int
	refOffset  int
	kind, op   string
	value      FormulaValue
	ref        formulaReference
	args       []*formulaNode
}
type formulaParser struct {
	ctx               context.Context
	text              string
	pos, nodes, depth int
	strictNames       bool
	preserveSingle    bool
	sourceNames       bool
}
type formulaEval struct {
	names       map[string]FormulaValue
	ctx         context.Context
	resolve     FormulaResolver
	cache       map[string]RangeValue
	steps       int
	textBytes   int
	approximate bool
	worksheet   bool
	now         time.Time
	date1904    bool
}

func Formula(ctx context.Context, expression string, resolve FormulaResolver) (Result, error) {
	if len(expression) > MaxExpression {
		return Result{}, errors.New("#NUM! 公式过长")
	}
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "=") {
		expression = expression[1:]
	}
	p := formulaParser{ctx: ctx, text: normalizeFormula(expression)}
	node, err := p.expression(0)
	if err != nil {
		return Result{}, err
	}
	p.space()
	if p.pos != len(p.text) {
		return Result{}, p.fail("公式不完整")
	}
	evaluator := formulaEval{ctx: ctx, resolve: resolve, cache: map[string]RangeValue{}, now: time.Now(), date1904: date1904FromContext(ctx)}
	v, err := evaluator.eval(node)
	if err != nil {
		return Result{}, err
	}
	switch v.Kind {
	case "number", "blank":
		if v.Kind == "blank" {
			v = NumberValue(new(big.Rat))
		}
		if err := bounded(v.Number); err != nil {
			return Result{}, err
		}
		result := Format(v.Number, -1, false)
		result.Kind = "number"
		if evaluator.approximate && !result.Approximate {
			result.Approximate = true
			result.Display += " ≈"
		}
		return result, nil
	case "boolean":
		s := "FALSE"
		if v.Bool {
			s = "TRUE"
		}
		return Result{Kind: "boolean", Value: s, Display: s, Copy: s, Exact: s, Steps: []Step{}, Used: []string{}}, nil
	case "text":
		if len(v.Text) > maxFormulaText {
			return Result{}, errors.New("#NUM! 文本结果过长")
		}
		return Result{Kind: "text", Value: v.Text, Display: v.Text, Copy: v.Text, Exact: v.Text, Steps: []Step{}, Used: []string{}}, nil
	case "range":
		return evaluator.arrayResult(v)
	default:
		return Result{}, errors.New("#VALUE! 范围需要用于函数")
	}
}

// Normalize keyboard symbols without changing quoted text or worksheet names.
func normalizeFormula(s string) string {
	var b strings.Builder
	var quote rune
	arrayDepth := 0
	for _, r := range s {
		if quote != 0 {
			b.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			b.WriteRune(r)
			continue
		}
		switch r {
		case '{':
			arrayDepth++
		case '}':
			arrayDepth--
		case '×', '＊':
			r = '*'
		case '÷', '／':
			r = '/'
		case '−', '－':
			r = '-'
		case '＋':
			r = '+'
		case '％':
			r = '%'
		case '（':
			r = '('
		case '）':
			r = ')'
		case '，':
			r = ','
		case '；', ';':
			if arrayDepth > 0 {
				r = ';'
			} else {
				r = ','
			}
		case '\u3000', '\u00a0':
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}
func (p *formulaParser) fail(s string) error {
	return fmt.Errorf("#VALUE! %s（位置%d）", s, p.pos+1)
}
func (p *formulaParser) space() {
	for p.pos < len(p.text) {
		r, n := utf8.DecodeRuneInString(p.text[p.pos:])
		if !unicode.IsSpace(r) {
			break
		}
		p.pos += n
	}
}
func (p *formulaParser) newNode(n *formulaNode) (*formulaNode, error) {
	p.nodes++
	if p.nodes > 512 {
		return nil, errors.New("#NUM! 公式过于复杂")
	}
	return n, nil
}

func (p *formulaParser) referenceNode(ref formulaReference) (*formulaNode, error) {
	start := p.pos
	p.pos += ref.length
	node, err := p.newNode(&formulaNode{kind: "ref", ref: ref, refOffset: start})
	if err == nil && p.strictNames && ref.end == "" && p.pos < len(p.text) && p.text[p.pos] == '#' {
		p.pos++
		return p.newNode(&formulaNode{kind: "function", op: "ANCHORARRAY", args: []*formulaNode{node}, refOffset: start})
	}
	return node, err
}
func (p *formulaParser) expression(min int) (*formulaNode, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 40 {
		return nil, errors.New("#NUM! 公式嵌套过多")
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	left, err := p.prefix()
	if err != nil {
		return nil, err
	}
	for {
		p.space()
		if p.pos >= len(p.text) {
			break
		}
		op := string(p.text[p.pos])
		binding := 0
		if op == "%" {
			p.pos++
			left, err = p.newNode(&formulaNode{kind: "unary", op: "%", args: []*formulaNode{left}})
			if err != nil {
				return nil, err
			}
			continue
		}
		if p.pos+1 < len(p.text) {
			pair := p.text[p.pos : p.pos+2]
			if pair == "<=" || pair == ">=" || pair == "<>" {
				op = pair
			}
		}
		switch op {
		case "=", "<>", "<", ">", "<=", ">=":
			binding = 5
		case "&":
			binding = 10
		case "+", "-":
			binding = 20
		case "*", "/":
			binding = 30
		case "^":
			binding = 50
		}
		if binding == 0 || binding < min {
			break
		}
		p.pos += len(op)
		next := binding + 1
		right, err := p.expression(next)
		if err != nil {
			return nil, err
		}
		left, err = p.newNode(&formulaNode{kind: "binary", op: op, args: []*formulaNode{left, right}})
		if err != nil {
			return nil, err
		}
	}
	return left, nil
}
func (p *formulaParser) prefix() (*formulaNode, error) {
	p.space()
	if p.pos >= len(p.text) {
		return nil, p.fail("公式不完整")
	}
	ch := p.text[p.pos]
	if ch == '{' {
		p.pos++
		args := []*formulaNode{}
		rows, cols, current := 1, 0, 0
		for {
			arg, err := p.expression(0)
			if err != nil {
				return nil, err
			}
			if arg.kind != "value" && !(arg.kind == "unary" && arg.args[0].kind == "value") {
				return nil, p.fail("数组常量仅接受数字、文本或逻辑常量")
			}
			args = append(args, arg)
			current++
			p.space()
			if p.pos >= len(p.text) {
				return nil, p.fail("数组缺少结束括号")
			}
			next := p.text[p.pos]
			p.pos++
			if next == ';' || next == '}' {
				if cols == 0 {
					cols = current
				} else if cols != current {
					return nil, p.fail("数组各行列数不一致")
				}
				current = 0
				if next == '}' {
					return p.newNode(&formulaNode{kind: "array", args: args, rows: rows, cols: cols})
				}
				rows++
			} else if next != ',' {
				return nil, p.fail("数组分隔符无效")
			}
		}
	}
	if ch == '#' {
		for _, code := range []string{"#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A", "#NULL!", "#CALC!"} {
			if strings.HasPrefix(strings.ToUpper(p.text[p.pos:]), code) {
				p.pos += len(code)
				return p.newNode(&formulaNode{kind: "value", value: errorValue(errors.New(code))})
			}
		}
		return nil, p.fail("错误常量无效")
	}
	if ch == '+' || ch == '-' {
		p.pos++
		n, err := p.expression(51)
		if err != nil {
			return nil, err
		}
		return p.newNode(&formulaNode{kind: "unary", op: string(ch), args: []*formulaNode{n}})
	}
	if ch == '(' {
		p.pos++
		n, err := p.expression(0)
		if err != nil {
			return nil, err
		}
		p.space()
		if p.strictNames && p.pos < len(p.text) && p.text[p.pos] == ',' {
			var areas []*formulaNode
			for {
				switch n.kind {
				case "ref", "name":
					areas = append(areas, n)
				case "union":
					areas = append(areas, n.args...)
				default:
					return nil, p.fail("联合区域需要单元格或区域引用")
				}
				if p.pos >= len(p.text) || p.text[p.pos] != ',' {
					break
				}
				p.pos++
				n, err = p.expression(0)
				if err != nil {
					return nil, err
				}
				p.space()
			}
			n, err = p.newNode(&formulaNode{kind: "union", args: areas})
			if err != nil {
				return nil, err
			}
		}
		if p.pos >= len(p.text) || p.text[p.pos] != ')' {
			return nil, p.fail("缺少右括号")
		}
		p.pos++
		return n, nil
	}
	if ch == '"' {
		p.pos++
		var b strings.Builder
		for p.pos < len(p.text) {
			ch = p.text[p.pos]
			p.pos++
			if ch == '"' {
				if p.pos < len(p.text) && p.text[p.pos] == '"' {
					p.pos++
					b.WriteByte('"')
					continue
				}
				return p.newNode(&formulaNode{kind: "value", value: TextValue(b.String())})
			}
			b.WriteByte(ch)
		}
		return nil, p.fail("缺少文本结束引号")
	}
	if digit(ch) || ch == '.' {
		if ref, ok := reference(p.text[p.pos:]); ok && ref.end != "" {
			return p.referenceNode(ref)
		}
		start := p.pos
		p.pos++
		for p.pos < len(p.text) && (digit(p.text[p.pos]) || p.text[p.pos] == '.') {
			p.pos++
		}
		if p.pos < len(p.text) && (p.text[p.pos] == 'e' || p.text[p.pos] == 'E') {
			p.pos++
			if p.pos < len(p.text) && (p.text[p.pos] == '+' || p.text[p.pos] == '-') {
				p.pos++
			}
			for p.pos < len(p.text) && digit(p.text[p.pos]) {
				p.pos++
			}
		}
		n, err := Canonical(p.text[start:p.pos])
		if err != nil {
			return nil, p.fail("数字无效")
		}
		return p.newNode(&formulaNode{kind: "value", value: NumberValue(n)})
	}
	if p.sourceNames {
		if length := structuredToken(p.text[p.pos:]); length > 0 {
			start := p.pos
			p.pos += length
			return p.newNode(&formulaNode{kind: "name", op: p.text[start:p.pos], refOffset: start})
		}
	}
	if ref, ok := reference(p.text[p.pos:]); ok {
		return p.referenceNode(ref)
	}
	if letter(ch) || ch == '_' || p.sourceNames && (ch >= utf8.RuneSelf || ch == '\\') {
		start := p.pos
		for p.pos < len(p.text) {
			r, n := utf8.DecodeRuneInString(p.text[p.pos:])
			if !(r < utf8.RuneSelf && (letter(byte(r)) || digit(byte(r)) || r == '_' || r == '.') ||
				p.sourceNames && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\\' && p.pos == start)) {
				break
			}
			p.pos += n
		}
		if p.pos == start {
			return nil, p.fail("名称无效")
		}
		name := strings.ToUpper(p.text[start:p.pos])
		if !p.strictNames {
			name = strings.TrimPrefix(strings.TrimPrefix(name, "_XLFN."), "_XLWS.")
		} else if prefix, bare, ok := worksheetStoragePrefix(name); ok && name == prefix+bare {
			name = bare
		}
		p.space()
		if p.pos >= len(p.text) || p.text[p.pos] != '(' {
			if name == "TRUE" || name == "FALSE" {
				return p.newNode(&formulaNode{kind: "value", value: BoolValue(name == "TRUE")})
			}
			return p.newNode(&formulaNode{kind: "name", op: name, refOffset: start})
		}
		if _, ok := formulaFunctions[name]; !ok && !(p.preserveSingle && (name == "ANCHORARRAY" || name == "_XLFN.SINGLE" || name == "SUBTOTAL")) {
			return nil, fmt.Errorf("#NAME? %s", name)
		}
		p.pos++
		args := []*formulaNode{}
		p.space()
		if p.pos < len(p.text) && p.text[p.pos] == ')' {
			p.pos++
			return p.newNode(&formulaNode{kind: "function", op: name, args: args, refOffset: start})
		}
		for {
			p.space()
			var arg *formulaNode
			var err error
			if p.pos < len(p.text) && (p.text[p.pos] == ',' || p.text[p.pos] == ')') {
				arg = &formulaNode{kind: "value", value: BlankValue()}
			} else {
				arg, err = p.expression(0)
				if err != nil {
					return nil, err
				}
			}
			args = append(args, arg)
			if len(args) > 128 {
				return nil, errors.New("#NUM! 函数参数过多")
			}
			p.space()
			if p.pos >= len(p.text) {
				return nil, p.fail("缺少右括号")
			}
			ch = p.text[p.pos]
			p.pos++
			if ch == ')' {
				break
			}
			if ch != ',' {
				return nil, p.fail("参数需要逗号")
			}
		}
		return p.newNode(&formulaNode{kind: "function", op: name, args: args, refOffset: start})
	}
	return nil, p.fail("无法识别")
}
func (e *formulaEval) get(ref formulaReference) (RangeValue, error) {
	key := ref.sheet + "\x00" + ref.start + ":" + ref.end
	if cached, ok := e.cache[key]; ok {
		return cached, nil
	}
	if len(e.cache) >= MaxOperands {
		return RangeValue{}, errors.New("#NUM! 引用过多")
	}
	if e.resolve == nil {
		return RangeValue{}, errors.New("#REF! 请先打开工作表")
	}
	value, err := e.resolve(ref.sheet, ref.start, ref.end)
	if err != nil {
		return value, err
	}
	if value.Rows == 0 || value.Cols == 0 {
		c1, r1 := formulaCoordinates(ref.start)
		c2, r2 := formulaCoordinates(ref.end)
		if ref.end == "" {
			c2, r2 = c1, r1
		}
		if c1 > 0 && r1 > 0 && c2 > 0 && r2 > 0 {
			value.FirstRow = min(r1, r2)
			value.FirstCol = min(c1, c2)
			value.Rows = absInt(r2-r1) + 1
			value.Cols = absInt(c2-c1) + 1
		}
	}
	if value.Scalar != nil {
		e.textBytes += len(value.Scalar.Text)
		if e.textBytes > 32<<20 {
			return RangeValue{}, errors.New("#NUM! 引用文本过多")
		}
	}
	e.cache[key] = value
	return value, nil
}
func (e *formulaEval) eval(n *formulaNode) (FormulaValue, error) {
	if err := e.ctx.Err(); err != nil {
		return FormulaValue{}, err
	}
	e.steps++
	if e.steps > 1024 {
		return FormulaValue{}, errors.New("#NUM! 公式过于复杂")
	}
	switch n.kind {
	case "value":
		if e.worksheet && n.value.Kind == "number" {
			number, _ := n.value.Number.Float64()
			return worksheetNumber(number)
		}
		return n.value, valueFault(n.value)
	case "array":
		values := make([]FormulaValue, len(n.args))
		for i, arg := range n.args {
			v, err := e.eval(arg)
			if err != nil {
				if e.ctx.Err() != nil {
					return FormulaValue{}, e.ctx.Err()
				}
				v = errorValue(err)
			}
			values[i] = v
		}
		return arrayValue(values, n.rows, n.cols), nil
	case "name", "localName":
		if value, ok := e.names[n.op]; ok {
			return value, nil
		}
		return FormulaValue{}, fmt.Errorf("#NAME? %s", n.op)
	case "ref":
		if n.ref.start == "#REF!" {
			return FormulaValue{}, errors.New("#REF!")
		}
		rv, err := e.get(n.ref)
		if err != nil {
			return FormulaValue{}, err
		}
		if n.ref.end != "" {
			return FormulaValue{Kind: "range", Range: &rv}, nil
		}
		if rv.Fault != nil {
			return FormulaValue{}, rv.Fault
		}
		if rv.Scalar != nil {
			return *rv.Scalar, nil
		}
		if rv.Text {
			return TextValue(""), nil
		}
		if rv.Sum == nil {
			return BlankValue(), nil
		}
		return NumberValue(rv.Sum), nil
	case "function":
		if e.worksheet && n.op == "ANCHORARRAY" {
			r, err := e.get(n.args[0].ref)
			if err != nil {
				return FormulaValue{}, err
			}
			if r.Spill == nil {
				return FormulaValue{}, errors.New("#REF!")
			}
			spill, err := r.Spill()
			return FormulaValue{Kind: "range", Range: &spill}, err
		}
		v, err := e.function(n)
		if e.worksheet && err == nil && v.Kind == "error" {
			return v, valueFault(v)
		}
		if e.worksheet && err == nil && v.Kind == "number" {
			number, _ := v.Number.Float64()
			return worksheetNumber(number)
		}
		return v, err
	case "unary":
		v, err := e.eval(n.args[0])
		if err != nil {
			return v, err
		}
		if v.Kind == "range" {
			return e.arrayUnary(n.op, v)
		}
		number, err := formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
		if n.op == "-" {
			number.Neg(number)
		}
		if n.op == "%" {
			if e.worksheet {
				number, _ := number.Float64()
				return worksheetNumber(number / 100)
			}
			number.Quo(number, big.NewRat(100, 1))
		}
		if err := bounded(number); err != nil {
			return FormulaValue{}, err
		}
		return NumberValue(number), nil
	case "binary":
		a, err := e.eval(n.args[0])
		if err != nil {
			return a, err
		}
		b, err := e.eval(n.args[1])
		if err != nil {
			return b, err
		}
		return e.binaryValue(n.op, a, b)
	}
	return FormulaValue{}, errors.New("#VALUE! 公式无效")
}
func (e *formulaEval) binaryValue(op string, a, b FormulaValue) (FormulaValue, error) {
	if a.Kind == "range" || b.Kind == "range" {
		return e.arrayBinary(op, a, b)
	}
	if err := valueFault(a); err != nil {
		return FormulaValue{}, err
	}
	if err := valueFault(b); err != nil {
		return FormulaValue{}, err
	}
	if op == "&" {
		s, err := e.operandText(a)
		if err != nil {
			return FormulaValue{}, err
		}
		t, err := e.operandText(b)
		if err != nil {
			return FormulaValue{}, err
		}
		if len(s)+len(t) > maxFormulaText {
			return FormulaValue{}, errors.New("#NUM! 文本结果过长")
		}
		return TextValue(s + t), nil
	}
	if strings.Contains(" = <> < > <= >= ", " "+op+" ") {
		comparison, err := compareFormulaValues(a, b)
		if err != nil {
			return FormulaValue{}, err
		}
		yes := false
		switch op {
		case "=":
			yes = comparison == 0
		case "<>":
			yes = comparison != 0
		case "<":
			yes = comparison < 0
		case ">":
			yes = comparison > 0
		case "<=":
			yes = comparison <= 0
		case ">=":
			yes = comparison >= 0
		}
		return BoolValue(yes), nil
	}
	x, err := formulaNumber(a)
	if err != nil {
		return FormulaValue{}, err
	}
	y, err := formulaNumber(b)
	if err != nil {
		return FormulaValue{}, err
	}
	if e.worksheet {
		return worksheetArithmetic(op, x, y)
	}
	if op == "^" {
		return formulaPower(e, x, y)
	}
	v, err := binaryArithmetic(op[0], x, y)
	if err != nil {
		return FormulaValue{}, err
	}
	return NumberValue(v), nil
}

func formulaNumber(v FormulaValue) (*big.Rat, error) {
	switch v.Kind {
	case "number":
		return new(big.Rat).Set(v.Number), nil
	case "blank":
		return new(big.Rat), nil
	case "boolean":
		if v.Bool {
			return big.NewRat(1, 1), nil
		}
		return new(big.Rat), nil
	case "text":
		s := strings.TrimSpace(v.Text)
		percent := strings.HasSuffix(s, "%")
		s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
		if !strings.Contains(s, "/") {
			if n, ok := accountingNumber(s); ok {
				if percent {
					n.Quo(n, big.NewRat(100, 1))
				}
				return n, nil
			}
		}
	}
	return nil, errors.New("#VALUE! 需要数值")
}
func formulaText(v FormulaValue) (string, error) {
	if err := valueFault(v); err != nil {
		return "", err
	}
	switch v.Kind {
	case "text":
		return v.Text, nil
	case "blank":
		return "", nil
	case "number":
		return ExactText(v.Number), nil
	case "boolean":
		if v.Bool {
			return "TRUE", nil
		}
		return "FALSE", nil
	}
	return "", errors.New("#VALUE! 需要单个值")
}
func formulaBool(v FormulaValue) (bool, error) {
	if err := valueFault(v); err != nil {
		return false, err
	}
	if v.Kind == "boolean" {
		return v.Bool, nil
	}
	if v.Kind == "blank" {
		return false, nil
	}
	if v.Kind == "number" {
		return v.Number.Sign() != 0, nil
	}
	if v.Kind == "text" {
		if strings.EqualFold(v.Text, "TRUE") {
			return true, nil
		}
		if strings.EqualFold(v.Text, "FALSE") {
			return false, nil
		}
	}
	return false, errors.New("#VALUE! 需要逻辑值")
}
func compareFormulaValues(a, b FormulaValue) (int, error) {
	if a.Kind == "range" || b.Kind == "range" {
		return 0, errors.New("#VALUE! 不能直接比较范围")
	}
	if a.Kind == "blank" {
		switch b.Kind {
		case "text":
			a = TextValue("")
		case "boolean":
			a = BoolValue(false)
		default:
			a = NumberValue(new(big.Rat))
		}
	}
	if b.Kind == "blank" {
		switch a.Kind {
		case "text":
			b = TextValue("")
		case "boolean":
			b = BoolValue(false)
		default:
			b = NumberValue(new(big.Rat))
		}
	}
	rank := map[string]int{"number": 0, "text": 1, "boolean": 2}
	if a.Kind != b.Kind {
		return rank[a.Kind] - rank[b.Kind], nil
	}
	switch a.Kind {
	case "number":
		return a.Number.Cmp(b.Number), nil
	case "text":
		return strings.Compare(strings.ToUpper(a.Text), strings.ToUpper(b.Text)), nil
	case "boolean":
		if a.Bool == b.Bool {
			return 0, nil
		}
		if a.Bool {
			return 1, nil
		}
		return -1, nil
	}
	return 0, nil
}
