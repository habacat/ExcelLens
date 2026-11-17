package calculator

import (
	"context"
	"math/big"
	"testing"
)

func TestOrderedLookupStopsAtFirstExactMatch(t *testing.T) {
	for _, expression := range []string{
		"=XLOOKUP(2,A1:A300000,B1:B300000)",
		"=MATCH(2,A1:A300000,0)",
		"=XMATCH(2,A1:A300000)",
	} {
		read := 0
		resolver := func(sheet, start, end string) (RangeValue, error) {
			r := RangeValue{Rows: 300000, Cols: 1, Ordered: true}
			r.Visit = func(emit func(int, FormulaValue) error) error {
				for i := 0; i < r.Rows; i++ {
					read++
					if err := emit(i, NumberValue(big.NewRat(int64(i+1), 1))); err != nil {
						return err
					}
				}
				return nil
			}
			r.At = func(i int) (FormulaValue, error) { return TextValue("000002"), nil }
			return r, nil
		}
		got, err := Formula(context.Background(), expression, resolver)
		want := "2"
		if expression[1] == 'X' && expression[2] == 'L' {
			want = "000002"
		}
		if err != nil || got.Display != want || read != 2 {
			t.Fatalf("%s: %+v %v, read %d", expression, got, err, read)
		}
	}
}

func TestUnorderedLookupsKeepFirstSourcePositionAndReverseKeepsLast(t *testing.T) {
	reads := 0
	resolver := func(sheet, start, end string) (RangeValue, error) {
		return RangeValue{Rows: 3, Cols: 1, Visit: func(emit func(int, FormulaValue) error) error {
			for _, at := range []int{2, 1, 0} {
				reads++
				if err := emit(at, NumberValue(big.NewRat(7, 1))); err != nil {
					return err
				}
			}
			return nil
		}}, nil
	}
	got, err := Formula(context.Background(), "=XMATCH(7,A1:A3)", resolver)
	if err != nil || got.Value != "1" || reads != 3 {
		t.Fatal(got, err, reads)
	}
	got, err = Formula(context.Background(), "=XMATCH(2,{1,2,2,3},0,-1)", nil)
	if err != nil || got.Value != "3" {
		t.Fatal(got, err)
	}
	got, err = Formula(context.Background(), "=XMATCH(2.5,{1,2,3,4},-1)", nil)
	if err != nil || got.Value != "2" {
		t.Fatal(got, err)
	}
}
