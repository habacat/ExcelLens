package searchdsl

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

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
