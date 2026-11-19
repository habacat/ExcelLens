package calculator

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

var errEditableFormula = errors.New("公式必须是使用当前工作簿 A1 引用的受支持普通公式")

type editableReference struct {
	from, to    int
	col, row    int
	absoluteCol bool
	absoluteRow bool
}

// EditableFormula describes a verified XLSX expression and the exact
// spans of its A1 references. Editing can bind the tree to a cell position.
type EditableFormula struct {
	Scalar      bool
	root        *formulaNode
	expression  string
	references  []editableReference
	tableTokens [][2]int
}

// ParseEditableFormula accepts verified, single-result formulas for ordinary
// cells. Array anchors and formulas using defined names use the same parser
// with the workbook's range ownership and name bindings.
func ParseEditableFormula(ctx context.Context, expression string, hasSheet func(string) bool) (EditableFormula, error) {
	body, ok := strings.CutPrefix(expression, "=")
	if !ok {
		return EditableFormula{}, errEditableFormula
	}
	formula, err := ParseLocalFormula(ctx, body, hasSheet, nil)
	if err == nil && !formula.Scalar {
		err = errEditableFormula
	}
	return formula, err
}

// Shift applies spreadsheet fill semantics to every reference, preserving
// sheet qualifiers, absolute axes, string literals and intervening syntax.
func (f EditableFormula) Shift(deltaRow, deltaCol int) (string, error) {
	if deltaRow == 0 && deltaCol == 0 {
		return f.expression, nil
	}
	var out strings.Builder
	pos := 0
	for _, ref := range f.references {
		row, col := ref.row, ref.col
		if row != 0 && !ref.absoluteRow {
			row += deltaRow
		}
		if col != 0 && !ref.absoluteCol {
			col += deltaCol
		}
		if ref.row != 0 && (row < 1 || row > formats.ExcelRows) || ref.col != 0 && (col < 1 || col > formats.ExcelColumns) {
			return "", errors.New("平移后的公式引用超出 XLSX 行列范围")
		}
		out.WriteString(f.expression[pos:ref.from])
		if row == ref.row && col == ref.col {
			out.WriteString(f.expression[ref.from:ref.to])
		} else {
			if col != 0 {
				if ref.absoluteCol {
					out.WriteByte('$')
				}
				out.WriteString(strings.TrimSuffix(formulaAddress(col, 1), "1"))
			}
			if row != 0 {
				if ref.absoluteRow {
					out.WriteByte('$')
				}
				out.WriteString(strconv.Itoa(row))
			}
		}
		pos = ref.to
	}
	out.WriteString(f.expression[pos:])
	if out.Len() > formats.EditFormulaBytes {
		return "", errEditableFormula
	}
	return out.String(), nil
}
