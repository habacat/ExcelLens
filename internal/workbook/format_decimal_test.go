package workbook

import (
	"math/big"
	"strconv"
	"strings"
	"testing"
)

func TestFixedDecimalSourceAndConditionalDisplaysUseOriginalDigits(t *testing.T) {
	for _, tc := range []struct{ raw, format, want string }{
		{"9007199254740993.31", "0.00", "9007199254740993.31"},
		{"9007199254740993.315", "0.00", "9007199254740993.32"},
		{"-9007199254740993.315", "#,##0.00;[Red](#,##0.00)", "(9,007,199,254,740,993.32)"},
		{"1.005", "0.00", "1.01"}, {"2.675", "0.00", "2.68"},
		{"9.995", "0.00", "10.00"}, {"0.005", "0.00", "0.01"},
		{"-0.0049", "0.00", "-0.00"}, {"-0.000", "0.00", "0.00"},
		{"1234.56789", `[$¥-804]#,##0.00"元"`, "¥1,234.57元"},
		{"0.12345", "0.00%", "12.35%"},
		{"90071992547409.9331", "0.00%", "9007199254740993.31%"},
		{"0", `0.00;(0.00);"-"??`, "-"},
		{"12", "00000.00", "00012.00"},
		{"9007199254740993.31", "[Red]General", "9007199254740993.31"},
		{"1e3", "[Red]General", "1000"},
		{"1.25", `[Red]"General"`, "General"},
		{"-1e-999", "0.00", "-1e-999"},
		{"1e1000000000", "0.00", "1e1000000000"},
	} {
		t.Run(tc.raw+"/"+tc.format, func(t *testing.T) {
			w := &Workbook{Styles: []StyleInfo{{NumFmtID: 164}}, NumFmts: map[int]string{164: tc.format}}
			cell := w.resolveCell(xmlCell{Ref: "A1", Value: tc.raw, HasValue: true}, 1, nil, true, true)
			if cell.Raw != tc.raw || cell.Display != tc.want || cell.Type != "" || cell.Style != 0 {
				t.Fatal("formatted source digits/type changed", cell, tc.want)
			}
			p := w.conditionalPresentation(cell, ConditionalFormatResult{Style: DifferentialStyle{NumberFormat: &DifferentialNumberFormat{ID: 165, Code: tc.format}}})
			if p.Display == nil || *p.Display != tc.want || cell.Raw != tc.raw || cell.Display != tc.want {
				t.Fatal("conditional formatter diverged from ordinary XF", p, cell)
			}
		})
	}
	for id, want := range map[int]string{1: "9007199254740993", 2: "9007199254740993.31", 3: "9,007,199,254,740,993", 4: "9,007,199,254,740,993.31", 9: "900719925474099331%", 10: "900719925474099331.00%", 44: "9,007,199,254,740,993.31"} {
		w := &Workbook{Styles: []StyleInfo{{NumFmtID: id}}}
		if got := w.formatNumeric("9007199254740993.31", 0); got != want {
			t.Fatal("built-in numeric format used rounded float digits", id, got, want)
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
		n, ok := parseConditionalDecimal(raw)
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
