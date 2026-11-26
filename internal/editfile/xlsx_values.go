package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
)

var xlsxTextEscapes = regexp.MustCompile(`(?i)_x[[:xdigit:]]{4}_`)

func ordinaryFormulaAttributes(el xml.StartElement) bool {
	for _, a := range el.Attr {
		if a.Name == (xml.Name{Local: "ca"}) && ruleBool(a.Value) {
			continue
		}
		if !nsDeclaration(a) && !(a.Name.Space == "" && a.Name.Local == "t" && a.Value == "normal") {
			return false
		}
	}
	return true
}

func readFormulaCache(kind, raw string, stored bool, shared []string) (*editdoc.Value, error) {
	switch kind {
	case "", "n", "b", "e", "s", "str":
	default:
		return nil, fmt.Errorf("%w：公式缓存类型 %s", ErrXLSX, kind)
	}
	if !stored || kind != "str" && strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if kind == "str" {
		return &editdoc.Value{Kind: "text", Raw: raw}, nil
	}
	value, err := xlsxOriginalValue(kind, raw, true, false, shared)
	return &value, err
}

// FormulaForFill parses the source notation before shifting. The batch then
// binds every name and reference in the destination sheet before confirmation.
func (l XLSXLayout) FormulaForFill(ctx context.Context, raw string) (calculator.EditableFormula, error) {
	if !l.Structured {
		return calculator.ParseEditableFormula(ctx, raw, l.HasSheet)
	}
	body, ok := strings.CutPrefix(raw, "=")
	if !ok || xlsxTextEscapes.MatchString(body) {
		return calculator.EditableFormula{}, ErrXLSX
	}
	return calculator.ParseLocalFormula(ctx, body, l.HasSheet, func(string) (bool, bool) { return false, true })
}

func (l XLSXLayout) validateFormula(ctx context.Context, raw string) error {
	if xlsxTextEscapes.MatchString(raw) {
		return fmt.Errorf("公式含保留的 XML 转义形式")
	}
	_, err := calculator.ParseEditableFormula(ctx, raw, l.HasSheet)
	return err
}

func xlsxOriginalValue(kind, raw string, stored, inline bool, shared []string) (editdoc.Value, error) {
	if !stored && !inline && (kind == "" || kind == "n" || kind == "str" || kind == "inlineStr") {
		return editdoc.Value{Kind: "blank"}, nil
	}
	switch kind {
	case "", "n":
		if raw == "" {
			return editdoc.Value{Kind: "blank"}, nil
		}
		if v := (editdoc.Value{Kind: "number", Raw: raw}); v.Valid() {
			return v, nil
		}
	case "d":
		if v := (editdoc.Value{Kind: "date", Raw: raw}); stored && v.Valid() {
			return v, nil
		}
	case "inlineStr":
		if inline && !stored {
			return editdoc.Value{Kind: "text", Raw: raw}, nil
		}
	case "str":
		// Some generators use CT_Cell/v with an explicit string type for
		// literal text. Keep its text identity, including an empty value.
		if stored && !inline {
			return editdoc.Value{Kind: "text", Raw: raw}, nil
		}
	case "s":
		i, err := strconv.Atoi(raw)
		if err == nil && i >= 0 && i < len(shared) && strconv.Itoa(i) == raw {
			return editdoc.Value{Kind: "text", Raw: shared[i]}, nil
		}
	case "b":
		if raw == "1" || raw == "0" {
			v := "FALSE"
			if raw == "1" {
				v = "TRUE"
			}
			return editdoc.Value{Kind: "boolean", Raw: v}, nil
		}
	case "e":
		if strings.HasPrefix(raw, "#") {
			return editdoc.Value{Kind: "error", Raw: raw}, nil
		}
	}
	return editdoc.Value{}, fmt.Errorf("%w：单元格原值或类型无法确认", ErrXLSX)
}

func (l XLSXLayout) validateValue(ctx context.Context, a editdoc.Address, value editdoc.Value, source SourceCell) error {
	return l.validateStoredValue(ctx, a, value, source, validateXLSXNumber)
}

func (l XLSXLayout) validateStoredValue(ctx context.Context, a editdoc.Address, value editdoc.Value, source SourceCell, numberCheck func(string) error) error {
	if !value.Valid() {
		return editdoc.ErrInvalid
	}
	if source.formula.kind == "array" && !source.formula.dynamic && !source.formula.area.single() && value.Kind != "formula" && value.Kind != "" {
		return errors.New("多格数组的锚点须保留公式")
	}
	switch value.Kind {
	case "formula":
		if l.Structured {
			if xlsxTextEscapes.MatchString(value.Raw) {
				return errors.New("公式含保留的 XML 转义形式")
			}
			p := xlsxPackage{layout: l, names: source.names}
			_, err := p.parseFormula(ctx, value.Raw[1:], a, nil)
			return err
		}
		if err := l.validateFormula(ctx, value.Raw); err != nil {
			p := xlsxPackage{layout: l, names: source.names}
			parsed, wider := p.parseFormula(ctx, strings.TrimPrefix(value.Raw, "="), a, nil)
			if wider != nil || parsed.Scalar || xlsxTextEscapes.MatchString(value.Raw) {
				return err
			}
		}
		return nil
	case "text":
		return validateXLSXText(value.Raw)
	case "number":
		return numberCheck(value.Raw)
	}
	return nil
}

func validateXLSXText(raw string) error {
	units := 0
	for _, r := range raw {
		units++
		if r >= 0x10000 {
			units++
		}
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0xfffe || r == 0xffff {
			return errors.New("文本含无法原样保存到 XLSX 的字符")
		}
	}
	if units > 32767 || xlsxTextEscapes.MatchString(raw) {
		return errors.New("文本超过 XLSX 单格限制或含保留的转义形式")
	}
	return nil
}

func validateXLSXNumber(raw string) error {
	if !(editdoc.Value{Kind: "number", Raw: raw}).Valid() {
		return editdoc.ErrInvalid
	}
	mantissa, _, _ := strings.Cut(strings.ToLower(strings.TrimLeft(raw, "+-")), "e")
	digits := strings.Trim(strings.ReplaceAll(mantissa, ".", ""), "0")
	if len(digits) > 15 {
		return errors.New("数值超出 Excel 可可靠保存的 15 位有效数字或数值范围；可改用文本")
	}
	return validateStoredDouble(raw)
}

// A calculated pivot result, like an Excel formula result, can need all
// binary64 digits to round-trip. Typed decimal inputs retain the 15-digit rule.
func validateStoredDouble(raw string) error {
	f, err := strconv.ParseFloat(raw, 64)
	mantissa, _, _ := strings.Cut(strings.ToLower(raw), "e")
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f == 0 && strings.ContainsAny(mantissa, "123456789") || f != 0 && math.Abs(f) < 0x1p-1022 {
		return errors.New("数值超出 Excel 可保存的数值范围")
	}
	return nil
}
