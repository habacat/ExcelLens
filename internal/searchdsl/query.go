// Package searchdsl parses an offline, read-only, row-oriented query language.
// It never evaluates Excel formulas, executes SQL, or performs filesystem access.
// Numeric comparisons use exact rational arithmetic and RE2 bounds regex work.
package searchdsl

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
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

var scientificNumericRe = regexp.MustCompile(`^([+-]?(?:\d+(?:\.\d*)?|\.\d+))[eE]([+-]?\d{1,3})$`)

func numeric(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if len(s) > 128 || strings.Contains(s, "/") || strings.HasPrefix(s, "0x") {
		return nil, false
	}
	if v, ok := new(big.Rat).SetString(s); ok {
		return v, true
	}
	// Excel numeric cache occasionally uses scientific notation.
	match := scientificNumericRe.FindStringSubmatch(s)
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

func evalOrderCmp(cmp int, op string) bool {
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
	default:
		return false
	}
}

func matchCmp(c Cell, v string, op string) bool {
	argNum, hasArgNum := numeric(v)
	if hasArgNum && c.Numeric && !c.MissingCache {
		n, ok := numeric(c.Raw)
		if !ok {
			return false
		}
		return evalOrderCmp(n.Cmp(argNum), op)
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
		return evalOrderCmp(cmp, op)
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
