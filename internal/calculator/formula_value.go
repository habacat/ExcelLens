package calculator

import (
	"errors"
	"math/big"
	"strings"

	"excellens/internal/excelformula"
)

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
		if b, ok := excelformula.BooleanText(v.Text); ok {
			return b, nil
		}
	}
	return false, errors.New("#VALUE! 需要逻辑值")
}

func formulaKindRank(kind string) int {
	switch kind {
	case "number":
		return 0
	case "text":
		return 1
	case "boolean":
		return 2
	default:
		return 0
	}
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
	if a.Kind != b.Kind {
		return formulaKindRank(a.Kind) - formulaKindRank(b.Kind), nil
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
