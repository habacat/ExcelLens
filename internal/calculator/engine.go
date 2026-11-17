// Package calculator evaluates bounded audit arithmetic with exact rationals.
// Expressions never execute JavaScript, spreadsheet formulas or system code.
package calculator

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"excellens/internal/analysis"
)

const MaxExpression = 4096
const MaxOperands = 128
const maxBits = 4096

type Request struct {
	Expression string            `json:"expression"`
	Operands   map[string]string `json:"operands"`
	Precision  int               `json:"precision"` // -1: exact finite decimal / 12+ place approximation
	Percent    bool              `json:"percent"`
}

type Step struct {
	Operation string `json:"operation"`
	Left      string `json:"left"`
	Right     string `json:"right,omitempty"`
	Result    string `json:"result"`
}
type Result struct {
	Kind        string   `json:"kind,omitempty"`
	Value       string   `json:"value"` // exact, suitable for another calculation
	Display     string   `json:"display"`
	Copy        string   `json:"copy"`
	Exact       string   `json:"exact"`
	Approximate bool     `json:"approximate"`
	Rounded     bool     `json:"rounded"`
	Zero        bool     `json:"zero"`
	Steps       []Step   `json:"steps"`
	Used        []string `json:"used"`
}

func Canonical(s string) (*big.Rat, error) {
	if len(s) == 0 || len(s) > 4096 {
		return nil, errors.New("数值长度超出计算范围")
	}
	var v *big.Rat
	if strings.Contains(s, "/") {
		parts := strings.Split(s, "/")
		if len(parts) != 2 {
			return nil, errors.New("无效精确数值")
		}
		n, a := new(big.Int).SetString(parts[0], 10)
		d, b := new(big.Int).SetString(parts[1], 10)
		if !a || !b || d.Sign() == 0 {
			return nil, errors.New("无效精确数值")
		}
		v = new(big.Rat).SetFrac(n, d)
	} else if n, ok := new(big.Int).SetString(s, 10); ok {
		v = new(big.Rat).SetInt(n)
	} else {
		var ok bool
		v, ok = analysis.Decimal(s)
		if !ok {
			return nil, errors.New("请输入有效数字")
		}
	}
	if err := bounded(v); err != nil {
		return nil, err
	}
	return v, nil
}
func bounded(v *big.Rat) error {
	if v.Num().BitLen() > maxBits || v.Denom().BitLen() > maxBits {
		return errors.New("结果精度或数量级超出计算范围，请拆分算式")
	}
	return nil
}

// ExactText only emits a decimal when it is exact; recurring results retain
// their fraction. Long finite decimals are bounded by the engine bit limit.
func ExactText(v *big.Rat) string {
	if v.IsInt() {
		return v.Num().String()
	}
	d := new(big.Int).Set(v.Denom())
	count := 0
	for _, p := range []int64{2, 5} {
		n := 0
		for new(big.Int).Mod(d, big.NewInt(p)).Sign() == 0 {
			d.Quo(d, big.NewInt(p))
			n++
		}
		count = max(count, n)
	}
	if d.Cmp(big.NewInt(1)) != 0 {
		return v.RatString()
	}
	return strings.TrimRight(strings.TrimRight(v.FloatString(count), "0"), ".")
}

func Format(v *big.Rat, precision int, percent bool) Result {
	result := Result{Value: v.RatString(), Exact: ExactText(v), Zero: v.Sign() == 0, Steps: []Step{}, Used: []string{}}
	displayed := new(big.Rat).Set(v)
	if percent {
		displayed.Mul(displayed, big.NewRat(100, 1))
	}
	if precision < 0 {
		result.Copy = ExactText(displayed)
		if strings.Contains(result.Copy, "/") {
			places := max(12, len(displayed.Denom().String())-len(new(big.Int).Abs(displayed.Num()).String())+14)
			result.Copy = strings.TrimRight(strings.TrimRight(displayed.FloatString(min(places, 80)), "0"), ".")
			if parsed, ok := new(big.Rat).SetString(result.Copy); ok && parsed.Sign() == 0 && displayed.Sign() != 0 {
				result.Copy = new(big.Float).SetPrec(128).SetRat(displayed).Text('g', 20)
			}
			result.Approximate = true
		}
	} else {
		result.Copy = displayed.FloatString(precision)
		back, ok := new(big.Rat).SetString(result.Copy)
		result.Rounded = !ok || back.Cmp(displayed) != 0
	}
	if strings.HasPrefix(result.Copy, "-") && strings.Trim(result.Copy[1:], "0.") == "" {
		result.Copy = result.Copy[1:]
	}
	if percent {
		result.Copy += "%"
	}
	result.Display = result.Copy
	if result.Approximate {
		result.Display = "≈ " + result.Display
	}
	return result
}

type parser struct {
	ctx               context.Context
	text              string
	pos, nodes, depth int
	operands          map[string]string
	steps             []Step
	used              []string
	seen              map[string]bool
}

func Evaluate(ctx context.Context, req Request) (Result, error) {
	if len(req.Expression) > MaxExpression || len(req.Operands) > MaxOperands {
		return Result{}, errors.New("算式过长或取数项超过128项")
	}
	if req.Precision < -1 || req.Precision > 20 {
		return Result{}, errors.New("显示小数位必须在0至20之间，或选择原精度")
	}
	p := parser{ctx: ctx, text: strings.NewReplacer("×", "*", "÷", "/", "−", "-", "＋", "+", "－", "-", "＊", "*", "／", "/", "％", "%", "（", "(", "）", ")", "，", ",").Replace(strings.TrimSpace(req.Expression)), operands: req.Operands, seen: map[string]bool{}}
	if p.text == "" {
		return Result{}, errors.New("请点选单元格或输入算式")
	}
	v, err := p.expression(0)
	if err != nil {
		return Result{}, err
	}
	p.space()
	if p.pos != len(p.text) {
		return Result{}, p.fail("此处缺少运算符或括号不匹配")
	}
	result := Format(v, req.Precision, req.Percent)
	result.Steps = p.steps
	result.Used = p.used
	if result.Steps == nil {
		result.Steps = []Step{}
	}
	if result.Used == nil {
		result.Used = []string{}
	}
	return result, nil
}
func (p *parser) fail(message string) error { return fmt.Errorf("%s（位置 %d）", message, p.pos+1) }
func (p *parser) space() {
	for p.pos < len(p.text) && unicode.IsSpace(rune(p.text[p.pos])) {
		p.pos++
	}
}
func (p *parser) record(op string, a, b, result *big.Rat) error {
	if len(p.steps) >= 256 {
		return errors.New("单条算式最多256个运算步骤")
	}
	if err := bounded(result); err != nil {
		return err
	}
	step := Step{Operation: op, Left: ExactText(a), Result: ExactText(result)}
	if b != nil {
		step.Right = ExactText(b)
	}
	p.steps = append(p.steps, step)
	return nil
}
func (p *parser) expression(minimum int) (*big.Rat, error) {
	p.depth++
	defer func() { p.depth-- }()
	p.nodes++
	if p.depth > 40 || p.nodes > 256 {
		return nil, p.fail("括号嵌套或运算步骤过多")
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
		op := p.text[p.pos]
		if op == '%' {
			p.pos++
			old := new(big.Rat).Set(left)
			left.Quo(left, big.NewRat(100, 1))
			if err := p.record("%", old, nil, left); err != nil {
				return nil, err
			}
			continue
		}
		binding := map[byte]int{'+': 10, '-': 10, '*': 20, '/': 20, '^': 30}[op]
		if binding == 0 || binding < minimum {
			break
		}
		p.pos++
		next := binding + 1
		right, e := p.expression(next)
		if e != nil {
			return nil, e
		}
		old := new(big.Rat).Set(left)
		left, err = binaryArithmetic(op, left, right)
		if err != nil {
			return nil, p.fail(err.Error())
		}
		if err := p.record(string(op), old, right, left); err != nil {
			return nil, err
		}
	}
	return left, nil
}
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
func (p *parser) prefix() (*big.Rat, error) {
	p.space()
	if p.pos >= len(p.text) {
		return nil, p.fail("算式尚未完成")
	}
	c := p.text[p.pos]
	if c == '+' || c == '-' {
		p.pos++
		v, e := p.expression(31)
		if e != nil {
			return nil, e
		}
		if c == '-' {
			old := new(big.Rat).Set(v)
			v.Neg(v)
			if err := p.record("取负", old, nil, v); err != nil {
				return nil, err
			}
		}
		return v, nil
	}
	if c == '(' {
		p.pos++
		v, e := p.expression(0)
		if e != nil {
			return nil, e
		}
		p.space()
		if p.pos >= len(p.text) || p.text[p.pos] != ')' {
			return nil, p.fail("缺少右括号")
		}
		p.pos++
		return v, nil
	}
	if c == '[' {
		p.pos++
		start := p.pos
		for p.pos < len(p.text) && p.text[p.pos] >= '0' && p.text[p.pos] <= '9' {
			p.pos++
		}
		id := p.text[start:p.pos]
		if id == "" || len(id) > 6 || p.pos >= len(p.text) || p.text[p.pos] != ']' {
			return nil, p.fail("取数编号无效")
		}
		p.pos++
		raw, ok := p.operands[id]
		if !ok {
			return nil, p.fail("取数项[" + id + "]不存在或尚未就绪")
		}
		v, e := Canonical(raw)
		if e != nil {
			return nil, e
		}
		if !p.seen[id] {
			p.seen[id] = true
			p.used = append(p.used, id)
		}
		return v, nil
	}
	if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
		return p.function()
	}
	start := p.pos
	dot, exponent := false, false
	for p.pos < len(p.text) {
		c = p.text[p.pos]
		if c >= '0' && c <= '9' {
			p.pos++
			continue
		}
		if c == '.' && !dot && !exponent {
			dot = true
			p.pos++
			continue
		}
		if (c == 'e' || c == 'E') && !exponent {
			exponent = true
			p.pos++
			if p.pos < len(p.text) && (p.text[p.pos] == '+' || p.text[p.pos] == '-') {
				p.pos++
			}
			continue
		}
		break
	}
	if p.pos == start {
		return nil, p.fail("请输入数字、括号或受支持的函数")
	}
	v, ok := analysis.Decimal(p.text[start:p.pos])
	if !ok {
		return nil, p.fail("数字格式无效；算式中请使用小数点，千位金额可从表格取数")
	}
	if e := bounded(v); e != nil {
		return nil, e
	}
	return v, nil
}
func (p *parser) function() (*big.Rat, error) {
	start := p.pos
	for p.pos < len(p.text) && ((p.text[p.pos] >= 'A' && p.text[p.pos] <= 'Z') || (p.text[p.pos] >= 'a' && p.text[p.pos] <= 'z')) {
		p.pos++
	}
	name := strings.ToUpper(p.text[start:p.pos])
	p.space()
	if p.pos >= len(p.text) || p.text[p.pos] != '(' {
		return nil, p.fail("函数后需要括号")
	}
	p.pos++
	var args []*big.Rat
	for {
		v, e := p.expression(0)
		if e != nil {
			return nil, e
		}
		args = append(args, v)
		if len(args) > 128 {
			return nil, p.fail("函数参数过多")
		}
		p.space()
		if p.pos >= len(p.text) {
			return nil, p.fail("函数缺少右括号")
		}
		c := p.text[p.pos]
		p.pos++
		if c == ')' {
			break
		}
		if c != ',' {
			return nil, p.fail("函数参数之间需要逗号")
		}
	}
	v := new(big.Rat).Set(args[0])
	switch name {
	case "ABS":
		if len(args) != 1 {
			return nil, p.fail("ABS需要1个参数")
		}
		v.Abs(v)
	case "SUM", "AVG", "AVERAGE", "MIN", "MAX":
		for _, a := range args[1:] {
			switch name {
			case "SUM", "AVG", "AVERAGE":
				v.Add(v, a)
			case "MIN":
				if a.Cmp(v) < 0 {
					v.Set(a)
				}
			case "MAX":
				if a.Cmp(v) > 0 {
					v.Set(a)
				}
			}
		}
		if name == "AVG" || name == "AVERAGE" {
			v.Quo(v, new(big.Rat).SetInt64(int64(len(args))))
		}
	case "ROUND", "ROUNDUP", "ROUNDDOWN":
		if len(args) != 2 || !args[1].IsInt() || !args[1].Num().IsInt64() {
			return nil, p.fail(name + "需要数值和整数小数位2个参数")
		}
		places := args[1].Num().Int64()
		if places < -20 || places > 20 {
			return nil, p.fail("舍入位数应在-20至20之间")
		}
		factor := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(abs(places)), nil))
		scaled := new(big.Rat).Set(v)
		if places >= 0 {
			scaled.Mul(scaled, factor)
		} else {
			scaled.Quo(scaled, factor)
		}
		q, rem := new(big.Int), new(big.Int)
		q.QuoRem(scaled.Num(), scaled.Denom(), rem)
		if rem.Sign() != 0 && (name == "ROUNDUP" || name == "ROUND" && new(big.Int).Lsh(new(big.Int).Abs(rem), 1).Cmp(scaled.Denom()) >= 0) {
			q.Add(q, big.NewInt(int64(scaled.Sign())))
		}
		v.SetInt(q)
		if places >= 0 {
			v.Quo(v, factor)
		} else {
			v.Mul(v, factor)
		}
	default:
		return nil, p.fail("支持 SUM、AVG、MIN、MAX、ABS、ROUND、ROUNDUP、ROUNDDOWN")
	}
	if err := p.record(name, args[0], nil, v); err != nil {
		return nil, err
	}
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = ExactText(a)
	}
	p.steps[len(p.steps)-1].Left = strings.Join(parts, ", ")
	return v, nil
}
