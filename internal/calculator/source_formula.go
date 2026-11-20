package calculator

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"excellens/internal/formats"
)

var (
	errLocalFormula = errors.New("公式依赖无法限定在当前工作簿")
	columnAxis      = regexp.MustCompile(`^\$?([A-Za-z]{1,3})$`)
	rowAxis         = regexp.MustCompile(`^\$?([1-9][0-9]{0,6})$`)
)

// XLSXFormulaDependencies checks the stored formula syntax and reports its
// explicit worksheet references without normalizing, evaluating or filling it.
// The bool reports whether the expression can only produce a single value.
func XLSXFormulaDependencies(ctx context.Context, body string, sheet func(string) bool) (bool, error) {
	return XLSXNamedFormulaDependencies(ctx, body, sheet, nil)
}

// XLSXNamedFormulaDependencies also binds bare names in their formula's sheet
// scope. The callback reports whether the name is scalar and whether it exists.
func XLSXNamedFormulaDependencies(ctx context.Context, body string, sheet func(string) bool, name func(string) (bool, bool)) (bool, error) {
	formula, err := ParseLocalFormula(ctx, body, sheet, name)
	return formula.Scalar, err
}

// ParseLocalFormula validates stored Excel syntax and records the exact spans
// of A1 references, including whole rows and columns, for shared formulas.
func ParseLocalFormula(ctx context.Context, body string, sheet func(string) bool, name func(string) (bool, bool)) (EditableFormula, error) {
	var formula EditableFormula
	if body == "" || len(body) >= formats.EditFormulaBytes || !utf8.ValidString(body) || strings.TrimSpace(body) != body {
		return formula, errLocalFormula
	}
	var quote rune
	for _, r := range body {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xfffe || r == 0xffff {
			return formula, errLocalFormula
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
		} else if r == '\'' || r == '"' {
			quote = r
		} else if unicode.IsSpace(r) && !strings.ContainsRune(" \t\r\n", r) {
			return formula, errLocalFormula
		}
	}
	p := formulaParser{ctx: ctx, text: body, strictNames: true, preserveSingle: true, sourceNames: name != nil}
	node, err := p.expression(0)
	if err != nil {
		return formula, err
	}
	p.space()
	if p.pos != len(body) {
		return formula, errLocalFormula
	}
	formula.expression = "=" + body
	if err := bindLetNames(node, nil); err != nil {
		return formula, err
	}
	if err := checkLocalFormula(node, body, sheet, name, &formula); err != nil {
		return EditableFormula{}, err
	}
	formula.Scalar = localFormulaScalar(node)
	formula.root = node
	return formula, nil
}

// Source ranges do not by themselves make a lookup or conditional aggregate
// spill. Its search value, criteria and optional selectors can be arrays.
func localFormulaScalar(node *formulaNode) bool {
	args := node.args
	switch node.kind {
	case "localName":
		return node.rows == 1
	case "ref", "array":
		row, col := singleFormulaAxes(node)
		return row && col
	case "value":
		return true
	case "function":
		if node.op == "LET" {
			return localFormulaScalar(args[len(args)-1])
		}
		if node.op == "XLOOKUP" && len(args) >= 3 {
			lr, lc := singleFormulaAxes(args[1])
			rr, rc := singleFormulaAxes(args[2])
			return localFormulaScalar(args[0]) && (lc && rc || lr && rr) && (len(args) < 4 || localFormulaScalar(args[3]))
		}
		if _, _, ok := worksheetArrayName(node.op); ok {
			return false
		}
		if node.op == "TRANSPOSE" {
			return len(args) == 1 && localFormulaScalar(args[0])
		}
		switch node.op {
		case "SUBTOTAL", "_XLFN.SINGLE", "SUM", "AVERAGE", "MIN", "MAX", "COUNT", "COUNTA", "AND", "OR", "PRODUCT", "SUMPRODUCT", "ROWS", "COLUMNS":
			return true
		case "ROW", "COLUMN":
			if len(args) == 0 {
				return true
			}
			row, col := singleFormulaAxes(args[0])
			return node.op == "ROW" && row || node.op == "COLUMN" && col
		case "INDEX":
			row, col := singleFormulaAxes(args[0])
			for _, index := range args[1:] {
				if !localFormulaScalar(index) {
					return false
				}
			}
			if len(args) == 2 {
				return row && col || (row || col) && positiveFormulaIndex(args[1])
			}
			return (row || positiveFormulaIndex(args[1])) && (col || positiveFormulaIndex(args[2]))
		case "SUMIF", "COUNTIF", "AVERAGEIF":
			return localFormulaScalar(args[1])
		case "SUMIFS", "AVERAGEIFS", "COUNTIFS":
			for i := 1 + len(args)%2; i < len(args); i += 2 {
				if !localFormulaScalar(args[i]) {
					return false
				}
			}
			return true
		case "VLOOKUP", "HLOOKUP", "MATCH":
			if !localFormulaScalar(args[0]) {
				return false
			}
			args = args[2:]
		}
	case "namedRange":
		return false
	}
	for i, child := range args {
		if node.kind == "function" && worksheetRangeArgument(node.op, i) {
			continue
		}
		if !localFormulaScalar(child) {
			return false
		}
	}
	return true
}

// The two axes are independent. A:A has one column, while 1:1 has one row.
func singleFormulaAxes(node *formulaNode) (oneRow, oneCol bool) {
	switch node.kind {
	case "array":
		return node.rows == 1, node.cols == 1
	case "value", "name": // multi-cell names have already been bound as namedRange
		return true, true
	case "ref":
		if node.ref.end == "" {
			return true, true
		}
		c1, r1 := formulaCoordinates(node.ref.start)
		c2, r2 := formulaCoordinates(node.ref.end)
		return r1 != 0 && r1 == r2, c1 != 0 && c1 == c2
	}
	return false, false
}

// Zero and omitted INDEX selectors can return a whole row or column. MATCH
// produces a one-based position or an Excel error, both safe on a scalar axis.
func positiveFormulaIndex(node *formulaNode) bool {
	return node.kind == "function" && node.op == "MATCH" ||
		node.kind == "value" && node.value.Kind == "number" && node.value.Number.IsInt() && node.value.Number.Sign() > 0
}

func checkLocalFormula(node *formulaNode, body string, sheet func(string) bool, name func(string) (bool, bool), formula *EditableFormula) error {
	switch node.kind {
	case "ref":
		ref := node.ref
		raw := body[node.refOffset : node.refOffset+ref.length]
		if ref.sheet != "" {
			i := strings.LastIndexByte(strings.TrimSuffix(raw, "#REF!"), '!')
			if i < 1 || strings.ContainsAny(ref.sheet, "[]:\\/?*") || sheet == nil || !sheet(ref.sheet) {
				return errLocalFormula
			}
			label := raw[:i]
			if !strings.HasPrefix(label, "'") {
				for index, r := range label {
					if !unicode.IsLetter(r) && r != '_' && (index == 0 || r != '.' && !unicode.IsDigit(r)) {
						return errLocalFormula
					}
				}
			}
			raw = raw[i+1:]
		}
		if raw == "#REF!" {
			node.kind, node.value = "value", errorValue(errors.New("#REF!"))
			return nil
		}
		first, last, pair := strings.Cut(raw, ":")
		kind := referenceAxis(first)
		if kind == "" || !pair && kind != "cell" || pair && referenceAxis(last) != kind {
			return errLocalFormula
		}
		from := node.refOffset + 1 + ref.length - len(raw)
		for _, address := range strings.Split(raw, ":") {
			col, row := formulaCoordinates(address)
			r := editableReference{from: from, to: from + len(address), col: col, row: row}
			r.absoluteCol = col != 0 && strings.HasPrefix(address, "$")
			r.absoluteRow = row != 0 && strings.Contains(address, "$")
			if col != 0 && row != 0 {
				r.absoluteRow = strings.Contains(strings.TrimPrefix(address, "$"), "$")
			}
			formula.references = append(formula.references, r)
			from += len(address) + 1
		}
	case "value", "binary", "unary", "array", "localName":
	case "name":
		if name == nil {
			return errLocalFormula
		}
		if strings.Contains(node.op, "[") {
			formula.tableTokens = append(formula.tableTokens, [2]int{node.refOffset + 1, node.refOffset + 1 + len(node.op)})
		}
		scalar, ok := name(node.op)
		if !ok {
			return errLocalFormula
		}
		if !scalar {
			node.kind = "namedRange"
		}
	case "function":
		switch node.op {
		case "INDIRECT":
			// A literal local A1 address has a provable workbook dependency.
			// Keep its text untouched on fill, while binding its target as a reference.
			if len(node.args) < 1 || len(node.args) > 2 || node.args[0].kind != "value" || node.args[0].value.Kind != "text" {
				return errLocalFormula
			}
			if len(node.args) == 2 && (node.args[1].kind != "value" || node.args[1].value.Kind != "boolean" || !node.args[1].value.Bool) {
				return errLocalFormula
			}
			raw := strings.TrimSpace(node.args[0].value.Text)
			ref, ok := reference(raw)
			if !ok || ref.length != len(raw) || ref.start == "#REF!" {
				return errLocalFormula
			}
			bound := &formulaNode{kind: "ref", ref: ref}
			if err := checkLocalFormula(bound, raw, sheet, name, &EditableFormula{}); err != nil {
				return err
			}
			node.kind, node.ref, node.args = "ref", ref, nil
			return nil
		case "ANCHORARRAY":
			if len(node.args) != 1 || node.args[0].kind != "ref" || node.args[0].ref.end != "" {
				return errLocalFormula
			}
		case "ROW", "COLUMN":
			// In a workbook the formula cell supplies the omitted reference.
			if len(node.args) > 1 {
				return errLocalFormula
			}
		case "SUBTOTAL":
			if len(node.args) < 2 {
				return errLocalFormula
			}
		case "_XLFN.SINGLE":
			if len(node.args) != 1 {
				return errLocalFormula
			}
		default:
			if !worksheetBuiltin(node.op) {
				return errLocalFormula
			}
			spec, ok := formulaFunctions[node.op]
			n := len(node.args)
			if !ok || n < spec.min || n > spec.max || (node.op == "SUMIFS" || node.op == "AVERAGEIFS") && n%2 != 1 || node.op == "COUNTIFS" && n%2 != 0 {
				return errLocalFormula
			}
		}
	default:
		return errLocalFormula
	}
	for _, child := range node.args {
		if child.kind == "union" && node.kind == "function" && (node.op == "SUM" || node.op == "AVERAGE") {
			for _, area := range child.args {
				if err := checkLocalFormula(area, body, sheet, name, formula); err != nil {
					return err
				}
			}
			continue
		}
		if err := checkLocalFormula(child, body, sheet, name, formula); err != nil {
			return err
		}
	}
	return nil
}

func referenceAxis(raw string) string {
	if ref, end := cellAddress(raw); end > 0 && end == len(raw) {
		c, r := formulaCoordinates(ref)
		if c >= 1 && c <= formats.ExcelColumns && r >= 1 && r <= formats.ExcelRows {
			return "cell"
		}
	}
	if m := columnAxis.FindStringSubmatch(raw); m != nil {
		c, _ := formulaCoordinates(strings.ToUpper(m[1]) + "1")
		if c >= 1 && c <= formats.ExcelColumns {
			return "column"
		}
	}
	if m := rowAxis.FindStringSubmatch(raw); m != nil {
		r, _ := strconv.Atoi(m[1])
		if r <= formats.ExcelRows {
			return "row"
		}
	}
	return ""
}
