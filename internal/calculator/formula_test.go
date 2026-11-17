package calculator

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
)

func TestWorksheetFormulaReferencesFunctionsAndExactArithmetic(t *testing.T) {
	resolve := func(sheet, start, end string) (RangeValue, error) {
		if sheet != "" && sheet != "财务表" && sheet != "O'Brien" {
			return RangeValue{}, errors.New("wrong sheet")
		}
		if end != "" {
			return RangeValue{Sum: big.NewRat(6, 10), Min: big.NewRat(1, 10), Max: big.NewRat(3, 10), Count: 3}, nil
		}
		value := map[string]string{"A1": "0.1", "B2": "0.2", "C3": "9007199254740993"}[start]
		if value == "" {
			return RangeValue{}, errors.New("wrong ref")
		}
		v, _ := new(big.Rat).SetString(value)
		return RangeValue{Sum: v, Min: v, Max: v, Count: 1}, nil
	}
	for _, tc := range []struct{ expr, want string }{
		{"=A1+B2", "0.3"}, {"=$A$1+$B2", "0.3"}, {"='财务表'!A1+B2", "0.3"}, {"='O''Brien'!A1+1", "1.1"}, {"=C3+1", "9007199254740994"},
		{"=SUM(A1:A3)", "0.6"}, {"=SUM(A1:A3,B2,1)", "1.8"}, {"=AVERAGE(A1:A3)", "0.2"}, {"=AVERAGE(A1:A3,1)", "0.4"},
		{"=MIN(A1:A3,1)", "0.1"}, {"=MAX(A1:A3,1)", "1"}, {"=ROUND(SUM(A1:A3)/7,2)", "0.09"}, {"=SUM('财务表'!$A$1:$A$3)*100", "60"},
		{"=2E-2+A1", "0.12"}, {"=1/3*3", "1"}, {"=(A1+B2)*2", "0.6"},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Formula(context.Background(), tc.expr, resolve)
			if err != nil || got.Exact != tc.want {
				t.Fatal(got, err, "want", tc.want)
			}
		})
	}
}

func TestWorksheetFormulaInvalidReferencesLimitsAndCancellation(t *testing.T) {
	for _, expr := range []string{"=A1:A3+1", "=fetch(1)", "=[1]+2", "='bad!A1", "=SUM(A1:A3", "=A0+1", "=A1B2+1", "=1e999999"} {
		if got, err := Formula(context.Background(), expr, nil); err == nil {
			t.Fatal("invalid accepted", expr, got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Formula(ctx, "=1+2", nil); err == nil {
		t.Fatal("cancelled work completed")
	}
	if _, err := Formula(context.Background(), "="+strings.Repeat("1+", 3000)+"1", nil); err == nil {
		t.Fatal("unbounded formula accepted")
	}
	count := 0
	_, err := Formula(context.Background(), "=A1+A1+A1", func(sheet, start, end string) (RangeValue, error) {
		count++
		return RangeValue{Sum: big.NewRat(1, 10)}, nil
	})
	if err != nil || count != 1 {
		t.Fatal("duplicate references reread", count, err)
	}
}
