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
