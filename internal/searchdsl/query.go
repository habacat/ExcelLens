// Package searchdsl parses an offline, read-only, row-oriented query language.
// It never evaluates Excel formulas, executes SQL, or performs filesystem access.
// Numeric comparisons use exact rational arithmetic and RE2 bounds regex work.
package searchdsl

import (
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxQueryLength = 2048

type Cell struct {
	Column       int
	Display      string
	Raw          string
	Formula      string
	Numeric      bool
	MissingCache bool
}

type Row struct {
	Sheet string
	Cells map[int]Cell
}

type node interface {
	eval(Row, *Schema) bool
	columns(*Schema, map[int]struct{})
	allColumns() bool
}
type andNode struct{ a, b node }
type orNode struct{ a, b node }
type notNode struct{ inner node }

func (n andNode) eval(r Row, s *Schema) bool            { return n.a.eval(r, s) && n.b.eval(r, s) }
func (n orNode) eval(r Row, s *Schema) bool             { return n.a.eval(r, s) || n.b.eval(r, s) }
func (n notNode) eval(r Row, s *Schema) bool            { return !n.inner.eval(r, s) }
func (n andNode) columns(s *Schema, c map[int]struct{}) { n.a.columns(s, c); n.b.columns(s, c) }
func (n orNode) columns(s *Schema, c map[int]struct{})  { n.a.columns(s, c); n.b.columns(s, c) }
func (n notNode) columns(s *Schema, c map[int]struct{}) { n.inner.columns(s, c) }
func (n andNode) allColumns() bool                      { return n.a.allColumns() || n.b.allColumns() }
func (n orNode) allColumns() bool                       { return n.a.allColumns() || n.b.allColumns() }
func (n notNode) allColumns() bool                      { return n.inner.allColumns() }

type pred struct {
	field, op string
	args      []string
	regex     *regexp.Regexp
	nums      []*big.Rat
	dates     []time.Time
}

func (n pred) allColumns() bool {
	return n.field == "" || n.field == "value" || n.field == "raw" || n.field == "formula"
}
func (n pred) columns(s *Schema, c map[int]struct{}) {
	if n.allColumns() || n.field == "sheet" {
		return
	}
	if col, ok := s.resolve(n.field); ok {
		c[col] = struct{}{}
	}
}
func numeric(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 128 || strings.Contains(s, "/") || strings.HasPrefix(s, "0x") {
		return nil, false
	}
	if v, ok := new(big.Rat).SetString(s); ok {
		return v, true
	}
	// Excel numeric cache occasionally uses scientific notation.
	re := regexp.MustCompile(`^([+-]?(?:\d+(?:\.\d*)?|\.\d+))[eE]([+-]?\d{1,3})$`)
	match := re.FindStringSubmatch(s)
	if match == nil {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(match[1])
	if !ok {
		return nil, false
	}
	exp, err := strconv.Atoi(match[2])
	if err != nil || exp > 308 || exp < -308 {
		return nil, false
	}
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(exp))), nil)
	if exp >= 0 {
		r.Mul(r, new(big.Rat).SetInt(pow))
	} else {
		r.Quo(r, new(big.Rat).SetInt(pow))
	}
	return r, true
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func date(s string) (time.Time, bool) {
	d, e := time.Parse("2006-01-02", strings.TrimSpace(s))
	return d, e == nil
}
func matchCmp(c Cell, v string, op string) bool {
	argNum, hasArgNum := numeric(v)
	if hasArgNum && c.Numeric && !c.MissingCache {
		n, ok := numeric(c.Raw)
		if !ok {
			return false
		}
		cmp := n.Cmp(argNum)
		switch op {
		case "=":
			return cmp == 0
		case "!=":
			return cmp != 0
		case ">":
			return cmp > 0
		case ">=":
			return cmp >= 0
		case "<":
			return cmp < 0
		case "<=":
			return cmp <= 0
		}
	}
	argDate, hasArgDate := date(v)
	if hasArgDate {
		val, ok := date(c.Display)
		if !ok {
			val, ok = date(c.Raw)
		}
		if !ok {
			return false
		}
		cmp := 0
		if val.Before(argDate) {
			cmp = -1
		} else if val.After(argDate) {
			cmp = 1
		}
		switch op {
		case "=":
			return cmp == 0
		case "!=":
			return cmp != 0
		case ">":
			return cmp > 0
		case ">=":
			return cmp >= 0
		case "<":
			return cmp < 0
		case "<=":
			return cmp <= 0
		}
	}
	// Exact text equality is case-insensitive; ordered text comparisons are
	// deliberately unsupported rather than silently sorting formatted values.
	if op == "=" {
		return strings.EqualFold(c.Display, v) || strings.EqualFold(c.Raw, v)
	}
	if op == "!=" {
		return !strings.EqualFold(c.Display, v) && !strings.EqualFold(c.Raw, v)
	}
	return false
}
func (n pred) eval(r Row, s *Schema) bool {
	if n.field == "sheet" {
		return compareString(r.Sheet, n)
	}
	if n.field == "formula" || n.field == "raw" || n.field == "value" || n.field == "" {
		for _, c := range r.Cells {
			selected := c
			if n.field == "formula" {
				selected.Display = c.Formula
				selected.Raw = c.Formula
				selected.Numeric = false
			}
			if n.field == "raw" {
				selected.Display = c.Raw
				selected.Numeric = false
			}
			if matchesCell(selected, n) {
				return true
			}
		}
		return false
	}
	col, ok := s.resolve(n.field)
	if !ok {
		return false
	}
	c, ok := r.Cells[col]
	if !ok {
		return matchesCell(Cell{}, n)
	}
	return matchesCell(c, n)
}
func compareString(v string, n pred) bool { return matchesCell(Cell{Display: v, Raw: v}, n) }
func matchesCell(c Cell, n pred) bool {
	text := c.Display
	if text == "" {
		text = c.Raw
	}
	switch n.op {
	case "term", ":":
		return strings.Contains(strings.ToLower(text), strings.ToLower(n.args[0]))
	case "=", "!=", ">", ">=", "<", "<=":
		return matchCmp(c, n.args[0], n.op)
	case "regex":
		return n.regex != nil && n.regex.MatchString(text)
	case "empty":
		return c.Display == "" && c.Raw == "" && !c.MissingCache
	case "notempty":
		return c.Display != "" || c.Raw != ""
	case "in":
		for _, value := range n.args {
			if matchCmp(c, value, "=") {
				return true
			}
		}
		return false
	case "between":
		if len(n.args) != 2 {
			return false
		}
		return matchCmp(c, n.args[0], ">=") && matchCmp(c, n.args[1], "<=")
	}
	return false
}

type token struct {
	kind, value string
	pos         int
}

func lex(source string) ([]token, error) {
	out := make([]token, 0, 32)
	for i := 0; i < len(source); {
		r, w := utf8Rune(source[i:])
		if unicode.IsSpace(r) {
			i += w
			continue
		}
		start := i
		switch source[i] {
		case '(', ')', ',', ':', '~', '=':
			out = append(out, token{kind: string(source[i]), value: string(source[i]), pos: i})
			i++
		case '!', '>', '<':
			i++
			if i < len(source) && source[i] == '=' {
				i++
			}
			out = append(out, token{kind: source[start:i], value: source[start:i], pos: start})
		default:
			if source[i] == '[' {
				i++
				for i < len(source) && source[i] != ']' {
					i++
				}
				if i >= len(source) {
					return nil, fmt.Errorf("语法位置 %d: 列名缺少 ]", start+1)
				}
				val := strings.TrimSpace(source[start+1 : i])
				i++
				if val == "" {
					return nil, fmt.Errorf("语法位置 %d: 列名不能为空", start+1)
				}
				out = append(out, token{"field", val, start})
				continue
			}
			if source[i] == '"' || source[i] == '\'' {
				quote := source[i]
				i++
				var b strings.Builder
				closed := false
				for i < len(source) {
					c := source[i]
					i++
					if c == quote {
						closed = true
						break
					}
					if c == '\\' && i < len(source) {
						next := source[i]
						if next == quote || next == '\\' {
							b.WriteByte(next)
							i++
							continue
						}
					}
					b.WriteByte(c)
				}
				if !closed {
					return nil, fmt.Errorf("语法位置 %d: 引号未闭合", start+1)
				}
				out = append(out, token{"word", b.String(), start})
				continue
			}
			// '-' preceding a token is NOT; negative numbers stay together
			// as operator operands, e.g. C2>=-100.
			if source[i] == '-' && (i+1 < len(source) && !isNumberStart(source[i+1])) {
				out = append(out, token{"NOT", "NOT", i})
				i++
				continue
			}
			for i < len(source) {
				c := source[i]
				if c == '(' || c == ')' || c == ',' || c == ':' || c == '~' || c == '=' || c == '!' || c == '>' || c == '<' || c == '[' || c == ']' || c == '"' || c == '\'' {
					break
				}
				rr, ww := utf8Rune(source[i:])
				if unicode.IsSpace(rr) {
					break
				}
				i += ww
			}
			if i == start {
				return nil, fmt.Errorf("语法位置 %d: 意外字符", start+1)
			}
			val := source[start:i]
			upper := strings.ToUpper(val)
			switch upper {
			case "AND", "OR", "NOT", "IN", "BETWEEN", "IS", "EMPTY":
				out = append(out, token{upper, upper, start})
			default:
				out = append(out, token{"word", val, start})
			}
		}
		if len(out) > 256 {
			return nil, fmt.Errorf("查询条件过多（最多256个词元）")
		}
	}
	out = append(out, token{"EOF", "", len(source)})
	return out, nil
}
func utf8Rune(s string) (rune, int) { return utf8.DecodeRuneInString(s) }
func isNumberStart(c byte) bool     { return c >= '0' && c <= '9' || c == '.' }

type parser struct {
	tokens []token
	at     int
	depth  int
}

func (p *parser) cur() token { return p.tokens[p.at] }
func (p *parser) take(kind string) bool {
	if p.cur().kind == kind {
		p.at++
		return true
	}
	return false
}
func (p *parser) require(kind string) (token, error) {
	t := p.cur()
	if t.kind != kind {
		return t, fmt.Errorf("语法位置 %d: 应输入 %s", t.pos+1, kind)
	}
	p.at++
	return t, nil
}
func (p *parser) expression() (node, error) { return p.disjunction() }
func (p *parser) disjunction() (node, error) {
	left, err := p.conjunction()
	if err != nil {
		return nil, err
	}
	for p.take("OR") {
		right, e := p.conjunction()
		if e != nil {
			return nil, e
		}
		left = orNode{left, right}
	}
	return left, nil
}
func (p *parser) conjunction() (node, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for {
		if p.take("AND") {
			right, e := p.unary()
			if e != nil {
				return nil, e
			}
			left = andNode{left, right}
			continue
		}
		// Adjacent terms imply AND, without accepting a stray OR or ')'.
		switch p.cur().kind {
		case "word", "field", "(", "NOT":
			right, e := p.unary()
			if e != nil {
				return nil, e
			}
			left = andNode{left, right}
		default:
			return left, nil
		}
	}
}
func (p *parser) unary() (node, error) {
	if p.take("NOT") {
		inner, e := p.unary()
		if e != nil {
			return nil, e
		}
		return notNode{inner}, nil
	}
	if p.take("(") {
		p.depth++
		if p.depth > 24 {
			return nil, fmt.Errorf("括号嵌套最多24层")
		}
		inner, e := p.expression()
		if e != nil {
			return nil, e
		}
		if _, e = p.require(")"); e != nil {
			return nil, e
		}
		p.depth--
		return inner, nil
	}
	return p.predicate()
}
func (p *parser) predicate() (node, error) {
	field := p.cur()
	if field.kind != "word" && field.kind != "field" {
		return nil, fmt.Errorf("语法位置 %d: 需要关键词或列名", field.pos+1)
	}
	p.at++
	name := field.value
	op := "term"
	value := name
	if field.kind == "field" {
		if p.cur().kind == "EOF" || p.cur().kind == ")" || p.cur().kind == "OR" || p.cur().kind == "AND" {
			return nil, fmt.Errorf("语法位置 %d: 列名后缺少比较条件", p.cur().pos+1)
		}
	}
	switch p.cur().kind {
	case ":", "=", "!=", ">", ">=", "<", "<=", "~":
		op = p.cur().kind
		p.at++
		if op == "~" {
			op = "regex"
		}
		v, e := p.value()
		if e != nil {
			return nil, e
		}
		value = v
	case "IN":
		op = "in"
		p.at++
		if _, e := p.require("("); e != nil {
			return nil, e
		}
		vals := make([]string, 0, 8)
		for {
			v, e := p.value()
			if e != nil {
				return nil, e
			}
			vals = append(vals, v)
			if len(vals) > 128 {
				return nil, fmt.Errorf("IN 最多128项")
			}
			if !p.take(",") {
				break
			}
		}
		if _, e := p.require(")"); e != nil {
			return nil, e
		}
		return pred{field: name, op: op, args: vals}, nil
	case "BETWEEN":
		op = "between"
		p.at++
		lo, e := p.value()
		if e != nil {
			return nil, e
		}
		if _, e = p.require("AND"); e != nil {
			return nil, e
		}
		hi, e := p.value()
		if e != nil {
			return nil, e
		}
		return pred{field: name, op: op, args: []string{lo, hi}}, nil
	case "IS":
		p.at++
		op = "empty"
		if p.take("NOT") {
			op = "notempty"
		}
		if _, e := p.require("EMPTY"); e != nil {
			return nil, e
		}
		return pred{field: name, op: op}, nil
	default:
		if field.kind == "field" {
			return nil, fmt.Errorf("语法位置 %d: 列名后需要 :、=、IN、BETWEEN 或 IS EMPTY", p.cur().pos+1)
		}
		return pred{op: "term", args: []string{value}}, nil
	}
	if op == "regex" {
		if len(value) > 256 {
			return nil, fmt.Errorf("正则过长（最多256字符）")
		}
		re, e := regexp.Compile(value)
		if e != nil {
			return nil, fmt.Errorf("语法位置 %d: 正则无效：%v", field.pos+1, e)
		}
		return pred{field: name, op: op, regex: re, args: []string{value}}, nil
	}
	return pred{field: name, op: op, args: []string{value}}, nil
}
func (p *parser) value() (string, error) {
	t := p.cur()
	if t.kind != "word" && t.kind != "field" {
		return "", fmt.Errorf("语法位置 %d: 缺少查询值", t.pos+1)
	}
	p.at++
	return t.value, nil
}

type Query struct {
	root   node
	source string
}

func Parse(s string) (*Query, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("查询语句不能为空")
	}
	if len(s) > MaxQueryLength {
		return nil, fmt.Errorf("高级搜索最长%d字符", MaxQueryLength)
	}
	toks, e := lex(s)
	if e != nil {
		return nil, e
	}
	p := parser{tokens: toks}
	root, e := p.expression()
	if e != nil {
		return nil, e
	}
	if p.cur().kind != "EOF" {
		return nil, fmt.Errorf("语法位置 %d: 不需要的内容 %q", p.cur().pos+1, p.cur().value)
	}
	return &Query{root: root, source: s}, nil
}

type Schema struct {
	colNames map[string]int
	maxCol   int
}

// NewSchema uses header names case-insensitively, but spreadsheet column
// letters always resolve to their physical positions. Repeated headers
// can be disambiguated by using the column letter instead.
func NewSchema(headers map[int]string, maxCol int) *Schema {
	names := make(map[string]int, len(headers))
	duplicate := make(map[string]bool)
	for col, name := range headers {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, ok := names[key]; ok {
			duplicate[key] = true
		} else {
			names[key] = col
		}
	}
	for name := range duplicate {
		delete(names, name)
	}
	return &Schema{colNames: names, maxCol: maxCol}
}
func (s *Schema) resolve(f string) (int, bool) {
	name := strings.ToLower(strings.TrimSpace(f))
	if col, ok := s.colNames[name]; ok {
		return col, true
	}
	if len(name) == 0 || len(name) > 3 {
		return 0, false
	}
	col := 0
	for _, r := range strings.ToUpper(name) {
		if r < 'A' || r > 'Z' {
			return 0, false
		}
		col = col*26 + int(r-'A'+1)
	}
	return col, col >= 1 && col <= s.maxCol
}
func (q *Query) CheckSchema(s *Schema) error {
	var missing string
	var walk func(node)
	walk = func(n node) {
		switch x := n.(type) {
		case pred:
			if x.field != "" && x.field != "sheet" && x.field != "formula" && x.field != "raw" && x.field != "value" {
				if _, ok := s.resolve(x.field); !ok {
					missing = x.field
				}
			}
		case andNode:
			walk(x.a)
			walk(x.b)
		case orNode:
			walk(x.a)
			walk(x.b)
		case notNode:
			walk(x.inner)
		}
	}
	walk(q.root)
	if missing != "" {
		return fmt.Errorf("找不到列 %q；可使用列字母（例如 C:关键字）或方括号中的表头", missing)
	}
	return nil
}
func (q *Query) Columns(s *Schema) []int {
	if q.root.allColumns() {
		cols := make([]int, s.maxCol)
		for i := range cols {
			cols[i] = i + 1
		}
		return cols
	}
	used := map[int]struct{}{}
	q.root.columns(s, used)
	result := make([]int, 0, len(used))
	for col := range used {
		result = append(result, col)
	}
	sort.Ints(result)
	if len(result) == 0 {
		return []int{1}
	}
	return result
}
func (q *Query) Matches(row Row, s *Schema) bool { return q.root.eval(row, s) }
