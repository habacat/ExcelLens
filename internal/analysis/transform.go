package analysis

import (
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"excellens/internal/valueconv"
)

func ApplyTransforms(vals map[string]Value, steps []Transform) {
	for _, step := range steps {
		src := vals[step.Source]
		s := src.Text
		out := Value{Text: s, Numeric: src.Numeric, MissingCache: src.MissingCache, ConversionFailed: src.ConversionFailed, Textual: src.Textual}
		if step.Op == "map_text" {
			out.Display = src.Display
		}
		if src.MissingCache {
			out.Numeric = false
			vals[step.ID] = out
			continue
		}
		failed := func() {
			out.Text, out.Display = s, src.Display
			out.Numeric, out.ConversionFailed = false, true
		}
		switch step.Op {
		case "map_text":
			// Unknown, numeric, boolean and error inputs cannot accidentally
			// match a text identifier. An explicit prior text conversion can.
			out.Display = src.Display
			if src.Textual && !src.Numeric && !src.ConversionFailed {
				if text, matched := mappedText(s, step.Mapping); matched {
					out.Text, out.Display, out.Numeric, out.Textual = text, text, false, true
				}
			}
		case "text":
			out.Numeric = false
		case "date", "time", "datetime":
			out.Numeric = false
			if strings.TrimSpace(s) == "" || src.MissingCache {
				break
			}
			dateSource := s
			if src.Numeric && src.Display != "" {
				dateSource = src.Display
			}
			converted, err := valueconv.Convert(dateSource, step.Op, step.Arg)
			if err != nil {
				failed()
			} else {
				out.Text = converted
				out.Display = converted
			}
		case "trim":
			out.Text = strings.TrimSpace(s)
		case "upper":
			out.Text = strings.ToUpper(s)
			out.Numeric = false
		case "lower":
			out.Text = strings.ToLower(s)
			out.Numeric = false
		case "prefix":
			out.Text = step.Arg + s
			out.Numeric = false
		case "suffix":
			out.Text = s + step.Arg
			out.Numeric = false
		case "length":
			out.Text = fmt.Sprint(utf8.RuneCountInString(s))
			out.Numeric = true
		case "replace":
			parts := strings.SplitN(step.Arg, "=>", 2)
			if len(parts) == 2 {
				out.Text = strings.ReplaceAll(s, parts[0], parts[1])
			}
			out.Numeric = false
		case "year", "month", "yearmonth":
			out.Numeric = false
			if strings.TrimSpace(s) == "" {
				break
			}
			dateSource := s
			if src.Display != "" {
				dateSource = src.Display
			}
			value, ok := parseDate(dateSource)
			if ok {
				switch step.Op {
				case "year":
					out.Text = value.Format("2006")
				case "month":
					out.Text = value.Format("01")
				default:
					out.Text = value.Format("2006-01")
				}
			} else {
				failed()
			}
		case "number":
			out.Numeric = false
			if strings.TrimSpace(s) == "" || src.MissingCache {
				break
			}
			if d, ok := AccountingDecimal(s); ok {
				out.Text = decimalExact(d)
				out.Numeric = true
			} else {
				failed()
			}
		case "abs", "multiply", "divide":
			d, ok := AccountingDecimal(s)
			if !ok || (!src.Numeric && step.Op != "abs") {
				if strings.TrimSpace(s) != "" {
					failed()
				}
				out.Numeric = false
				break
			}
			if step.Op == "abs" {
				d.Abs(d)
			} else {
				factor, valid := Decimal(step.Arg)
				if !valid {
					failed()
					break
				}
				if step.Op == "divide" {
					if factor.Sign() == 0 {
						failed()
						break
					}
					d.Quo(d, factor)
				} else {
					d.Mul(d, factor)
				}
			}
			out.Text, out.Numeric = decimalExact(d), true
		}
		if step.Op != "map_text" && !out.MissingCache && !out.ConversionFailed {
			out.Textual = !out.Numeric
		}
		vals[step.ID] = out
	}
}
func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02", "2006/01/02", "2006/1/2", "2006.01.02", "2006年1月2日", "2006-01", "2006/01", "2006"} {
		if v, e := time.ParseInLocation(layout, s, time.Local); e == nil {
			return v, true
		}
	}
	return time.Time{}, false
}
func numericOrEmpty(v Value) (*big.Rat, bool) {
	if !v.Numeric || v.MissingCache {
		return nil, false
	}
	return AccountingDecimal(v.Text)
}
