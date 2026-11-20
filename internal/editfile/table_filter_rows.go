package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
)

type tableFilterElement struct {
	XMLName  xml.Name
	Value    string               `xml:"val,attr"`
	Operator string               `xml:"operator,attr"`
	Blank    string               `xml:"blank,attr"`
	And      string               `xml:"and,attr"`
	Items    []tableFilterElement `xml:",any"`
}
type tableFilterField struct {
	Index int                  `xml:"colId,attr"`
	Rules []tableFilterElement `xml:",any"`
}
type rowFilterTest struct {
	column int
	match  func(editdoc.Value) (bool, error)
}

// Only rows which were already hidden can be released by dropping a
// criterion. Extending a filtered table keeps Excel's visible appended rows.
func (m tableResize) filterVisibility(ctx context.Context, p *xlsxPackage, s rangeSheet, tableXML []byte) ([]byte, error) {
	shrinking := m.next.lastRow < m.old.lastRow
	var before struct {
		Fields []tableFilterField `xml:"autoFilter>filterColumn"`
	}
	if err := xml.Unmarshal(tableXML, &before); err != nil {
		return nil, err
	}
	removed := false
	var kept []tableFilterField
	for _, f := range before.Fields {
		if len(f.Rules) == 0 {
			continue
		}
		if m.next.colsSpan().has(m.old.firstCol + f.Index) {
			kept = append(kept, f)
		} else {
			removed = true
		}
	}
	if !removed && !shrinking {
		return s.data, nil
	}
	if !removed {
		kept = nil
	}
	var tests []rowFilterTest
	for _, f := range kept {
		if len(f.Rules) != 1 {
			return nil, errors.New("请先清除被移除字段的筛选，再调整表格范围")
		}
		match, err := f.Rules[0].predicate(p.date1904)
		if err != nil {
			return nil, err
		}
		tests = append(tests, rowFilterTest{m.old.firstCol + f.Index, match})
	}
	// Side-by-side filtered objects share the row's single hidden flag.
	body := m.body(m.old)
	for _, table := range p.layout.Tables {
		if table.Part != m.table.Part && table.Sheet == m.table.Sheet && table.Filtered && table.bounds().firstRow <= body.lastRow && table.bounds().lastRow >= body.firstRow {
			return nil, errors.New("请先清除同一行上其他表格的筛选")
		}
	}
	var ps []xmlPatch
	active := map[int]editdoc.Value{}
	hidden := map[int]bool{}
	st := worksheetStructure{hidden: hidden}
	cleared := map[int]bool{}
	last := 0
	check := func() error {
		if !removed || last < body.firstRow || last > body.lastRow || !hidden[last] {
			return nil
		}
		for _, f := range tests {
			ok, err := f.match(active[f.column])
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
		}
		cleared[last] = true
		return nil
	}
	st.visitCell = func(row, col int, c sheetCell) error {
		if row != last {
			if err := check(); err != nil {
				return err
			}
			clear(active)
			last = row
		}
		if !hidden[row] || row < body.firstRow || row > body.lastRow {
			return nil
		}
		for _, f := range tests {
			if f.column != col {
				continue
			}
			v := c.value
			if v.Kind == "formula" {
				if c.cache == nil {
					return errors.New("筛选字段公式缺少结果，请先重算")
				}
				v = *c.cache
			}
			active[col] = v
		}
		return nil
	}
	if _, err := p.scanSheet(ctx, s.data, s.sheet, nil, nil, nil, &st); err != nil {
		return nil, err
	}
	if err := check(); err != nil {
		return nil, err
	}
	err := walkXML(ctx, s.data, func(e xmlEvent) error {
		t, ok := e.token.(xml.StartElement)
		if !ok || t.Name.Space != mainNS {
			return nil
		}
		if e.local() == "row" {
			row, _ := strconv.Atoi(attribute(t, "", "r"))
			if !hidden[row] || row < body.firstRow || row > body.lastRow {
				return nil
			}
			outside := row > m.body(m.next).lastRow
			if shrinking && outside && m.table.Totals > 0 {
				return errors.New("汇总行会穿过筛选隐藏的明细，请先清除该表筛选")
			}
			if !outside && !cleared[row] && !(removed && len(tests) == 0) {
				return nil
			}
			drop, err := removeXMLAttributes(s.data, e.from, e.to, "hidden")
			ps = append(ps, drop...)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, s.data, ps)
}

func (rule tableFilterElement) predicate(date1904 bool) (func(editdoc.Value) (bool, error), error) {
	switch rule.XMLName.Local {
	case "filters":
		labels := map[string]bool{}
		for _, v := range rule.Items {
			if v.XMLName.Local != "filter" {
				return nil, errors.New("请先清除日期分组筛选，再移除其他筛选字段")
			}
			labels[strings.ToUpper(v.Value)] = true
		}
		return func(v editdoc.Value) (bool, error) {
			if v.Raw == "" {
				return isTrue(rule.Blank), nil
			}
			return labels[strings.ToUpper(v.Raw)], nil
		}, nil
	case "customFilters":
		var matchers []func(calculator.FormulaValue) bool
		for _, item := range rule.Items {
			if item.XMLName.Local != "customFilter" {
				return nil, ErrXLSX
			}
			test, err := calculator.FilterCriterion(item.Operator, item.Value)
			if err != nil {
				return nil, err
			}
			matchers = append(matchers, test)
		}
		and := isTrue(rule.And)
		return func(v editdoc.Value) (bool, error) {
			if v.Kind == "" {
				v.Kind = "blank"
			}
			value, err := calcOperand(v, date1904)
			if err != nil {
				return false, err
			}
			for _, test := range matchers {
				if match := test(value); match != and {
					return match, nil
				}
			}
			return and, nil
		}, nil
	default:
		return nil, errors.New("请先清除颜色、图标或动态筛选，再移除其他筛选字段")
	}
}
