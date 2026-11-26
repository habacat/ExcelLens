package excelformula

import (
	"strconv"
	"strings"
)

// Decimal represents a canonical base-10 value without float rounding or
// expanding hostile scientific exponents.
type Decimal struct {
	Digits   string
	Exponent int64
	Negative bool
}

// ParseDecimal parses an exact decimal or scientific literal without floating-point loss.
func ParseDecimal(raw string) (Decimal, bool) {
	s := raw
	negative := false
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		if s[0] == '-' {
			negative = true
		}
		s = s[1:]
	}
	var exponent int64
	if at := strings.IndexAny(s, "eE"); at >= 0 {
		var err error
		exponent, err = strconv.ParseInt(s[at+1:], 10, 32)
		if err != nil {
			return Decimal{}, false
		}
		s = s[:at]
	}
	point, digits := false, 0
	for _, ch := range s {
		if ch == '.' && !point {
			point = true
			continue
		}
		if ch < '0' || ch > '9' {
			return Decimal{}, false
		}
		digits++
		if point {
			exponent--
		}
	}
	if digits == 0 {
		return Decimal{}, false
	}
	s = strings.TrimLeft(strings.ReplaceAll(s, ".", ""), "0")
	if s == "" {
		return Decimal{Digits: "0"}, true
	}
	tail := len(s) - len(strings.TrimRight(s, "0"))
	exponent += int64(tail)
	s = s[:len(s)-tail]
	return Decimal{Digits: s, Exponent: exponent, Negative: negative}, true
}

// CompareDecimal compares two canonical decimals without constructing large powers of ten.
func CompareDecimal(a, b Decimal) int {
	az, bz := a.Digits == "0", b.Digits == "0"
	if az && bz {
		return 0
	}
	if a.Negative != b.Negative {
		if a.Negative {
			return -1
		}
		return 1
	}
	cmp := 0
	if az {
		cmp = -1
	} else if bz {
		cmp = 1
	} else {
		am, bm := int64(len(a.Digits))+a.Exponent, int64(len(b.Digits))+b.Exponent
		if am < bm {
			cmp = -1
		} else if am > bm {
			cmp = 1
		} else {
			for i := 0; i < max(len(a.Digits), len(b.Digits)); i++ {
				x, y := byte('0'), byte('0')
				if i < len(a.Digits) {
					x = a.Digits[i]
				}
				if i < len(b.Digits) {
					y = b.Digits[i]
				}
				if x < y {
					cmp = -1
					break
				}
				if x > y {
					cmp = 1
					break
				}
			}
		}
	}
	if a.Negative {
		return -cmp
	}
	return cmp
}
