package styles

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"excellens/internal/excelformula"
)

func TestNumberFormatFontColor(t *testing.T) {
	values := SectionTextColors(165, `#,##0.00;[Red](#,##0.00);[Blue]"-"`)
	if values[0] != "" || values[1] != "#FF0000" || values[2] != "#0000FF" {
		t.Fatalf("format color directives: %#v", values)
	}
	table := []StyleInfo{{SectionColors: values}}
	for _, tc := range []struct{ raw, want string }{{"-19.5", "#FF0000"}, {"0", "#0000FF"}, {"10", ""}} {
		if got := NumericTextColor(tc.raw, 0, table); got != tc.want {
			t.Errorf("%s color = %s, want %s", tc.raw, got, tc.want)
		}
	}
	if got := SectionTextColors(40, "")[1]; got != "#FF0000" {
		t.Errorf("builtin red negative: %s", got)
	}
}

func TestVisualColorTolerance(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"FF112233", "#112233"}, {"00AABBCC", "#AABBCC"}, {"#aAbBcC", "#AABBCC"}, {"broken", ""}, {"12345", ""},
	} {
		if got := ParseHexColor(tc.in); got != tc.want {
			t.Errorf("%s => %s, want %s", tc.in, got, tc.want)
		}
	}
	if got := TintHex("#000000", 1); got != "#FFFFFF" {
		t.Errorf("positive tint: %s", got)
	}
	if got := TintHex("#FFFFFF", -1); got != "#000000" {
		t.Errorf("negative tint: %s", got)
	}
	if got := TintHex("#112233", 0); got != "#112233" {
		t.Errorf("zero tint: %s", got)
	}
}

func TestFormatCustomNumberSignedZeroAndDates(t *testing.T) {
	if got := FormatCustomNumber(math.Copysign(0, -1), `0.00;[Red](0.00);"-"`, false); got != "-" {
		t.Fatalf("negative signed zero custom = %q", got)
	}
	if got := FormatCustomDate(45930.5, `m/d/yy h:mm:ss AM/PM`, false); got != "9/30/25 12:00:00 PM" {
		t.Fatalf("custom date = %q", got)
	}
	if got := FormatCustomDate(1.5, `[h]:mm:ss`, false); got != "36:00:00" {
		t.Fatalf("elapsed hours = %q", got)
	}
	if !CalendarDateNumberFormat(14, "") || CalendarDateNumberFormat(20, "") {
		t.Fatal("calendar date number format classification regressed")
	}
	for _, format := range []string{
		`0;-0;"-"??"元"`, `0;-0;"-"??"万"`, `0;-0;"-"??)`,
		`0;-0;"-"??%`, `0;-0;"-"??\元`,
	} {
		if dash, ok := formatZeroSectionDash(SplitFormatSections(format)[2]); ok {
			t.Fatalf("incorrectly dropped literal from %q to %q", format, dash)
		}
	}
}

func FuzzDecimalFixedDisplayRoundsOriginalDecimal(f *testing.F) {
	f.Add(int64(2675), int8(-3), uint8(2))
	f.Add(int64(900719925474099331), int8(-2), uint8(2))
	f.Add(int64(-9995), int8(-3), uint8(2))
	f.Fuzz(func(t *testing.T, coefficient int64, exponent int8, places uint8) {
		exp, decimals := int(exponent)%10, int(places)%11
		raw := strconv.FormatInt(coefficient, 10) + "e" + strconv.Itoa(exp)
		n, ok := excelformula.ParseDecimal(raw)
		if !ok {
			t.Fatal("valid decimal rejected", raw)
		}
		got, ok := decimalFixedAbs(n, decimals, 0, 1, false)
		if !ok {
			t.Fatal("bounded decimal refused", raw)
		}
		r, ok := new(big.Rat).SetString(raw)
		if !ok {
			t.Fatal(raw)
		}
		r.Abs(r)
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
		r.Mul(r, new(big.Rat).SetInt(scale))
		q, remainder := new(big.Int), new(big.Int)
		q.QuoRem(r.Num(), r.Denom(), remainder)
		if new(big.Int).Lsh(remainder, 1).Cmp(r.Denom()) >= 0 {
			q.Add(q, big.NewInt(1))
		}
		want := q.String()
		if len(want) <= decimals {
			want = strings.Repeat("0", decimals+1-len(want)) + want
		}
		if decimals > 0 {
			at := len(want) - decimals
			want = want[:at] + "." + want[at:]
		}
		if got != want {
			t.Fatal(raw, decimals, got, want)
		}
	})
}
