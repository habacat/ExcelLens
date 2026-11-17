package calculator

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestExcelOperatorsCriteriaAndOptionalArguments(t *testing.T) {
	for _, tc := range [][2]string{
		{`=2^3^2`, `64`}, {`=-2^2`, `4`}, {`=-(2^2)`, `-4`}, {`=2^-2`, `0.25`},
		{`=10^101`, `1e101`}, {`=COUNTIF(C1:C6,"#DIV/0!")`, `1`},
		{`=COUNTIF(C1:C6,"<>#DIV/0!")`, `5`}, {`=COUNTIF(C1:C6,"<>")`, `6`},
		{`=COUNTIF({0,1,TRUE,"x","2"},">0")`, `2`},
		{`=COUNTIF({0,1,TRUE,"x","2"},"<>0")`, `4`},
		{`=WORKDAY.INTL(DATE(2024,1,5),1,,DATE(2024,1,8))`, `45300`},
		{`=XLOOKUP(2,A1:A6,B1:B6,,,1)`, `上海`},
		{`=SUM(SEQUENCE(3,,2))`, `9`},
		{`=SUM(IFERROR(C1:C6,0))`, `8.6`},
		{`=SUM(--ISERROR(C1:C6))`, `1`},
		{`=SUM(ABS({-1;2;-3}))`, `6`},
		{`=SUM(VALUE({"1";"2";"3"}))`, `6`},
		{`=SUM(ROUND({1.005,2.345},2))`, `3.36`},
		{`=SUM(YEAR({45292,45658}))`, `4049`},
		{`=TYPE({1,2})`, `64`},
		{`=SUM(IFERROR(A1:A6,1/0))`, `21`},
		{`=SUM(IFERROR(ABS(SEQUENCE(1500,1,-750)),0))`, `562500`},
		{`=SUM(INDEX(A1:C5,,1))`, `15`},
		{`=SUM(INDEX(A1:C5,1,))`, `1.1`},
		{`=SUM(ROW(A3:A5))`, `12`}, {`=SUM(COLUMN(B3:D5))`, `9`},
		{`=YEAR(0)`, `1900`}, {`=MONTH(0)`, `1`}, {`=DAY(0)`, `0`},
		{`=YEARFRAC(DATE(2023,7,1),DATE(2024,7,1),1)`, `1`},
		{`=YEARFRAC(DATE(2023,1,1),DATE(2024,1,1),1)`, `1`},
		{`=TEXTBEFORE("a/b","/",,,,1/0)`, `a`},
		{`=MATCH(2,{1,2,2,3})`, `3`},
		{`=LEN(TEXTJOIN("",TRUE,IF(SEQUENCE(9000)>0,"a","")))`, `9000`},
	} {
		t.Run(tc[0], func(t *testing.T) {
			r, err := Formula(context.Background(), tc[0], extendedResolver)
			if err != nil {
				t.Fatal(err)
			}
			if r.Kind == "number" {
				got, e1 := Canonical(r.Value)
				want, e2 := Canonical(tc[1])
				if e1 != nil || e2 != nil || got.Cmp(want) != 0 {
					t.Fatal(r, tc[1])
				}
			} else if r.Display != tc[1] {
				t.Fatal(r, tc[1])
			}
		})
	}
	for _, expr := range []string{`=0^0`, `=0^-0.5`, `=(-2)^0.5`, `=2^100000`, `=DATEVALUE(45292)`, `=DATEVALUE("45292")`, `=LEN(TEXTJOIN("",TRUE,REPT(IF(SEQUENCE(1000)>0,"a",""),60000)))`} {
		if _, err := Formula(context.Background(), expr, nil); err == nil {
			t.Fatal("invalid accepted", expr)
		}
	}
	r, err := Formula(context.Background(), `=9^0.5`, nil)
	if err != nil || r.Value != "3" || !r.Approximate {
		t.Fatal(r, err)
	}
	for _, tc := range [][2]string{
		{`=IFNA({#N/A,#DIV/0!,2},0)`, "0\t#DIV/0!\t2"},
		{`=LEFT({"北京","上海"},1)`, "北\t上"},
		{`=IFERROR({1,#N/A},1/0)`, "1\t#DIV/0!"},
		{`=XLOOKUP(2,{1;2;3},{"a",10;"b",20;"c",30})`, "b\t20"},
		{`=XLOOKUP(2,{1,2,3},{"a","b","c";10,20,30})`, "b\n20"},
	} {
		r, err := Formula(context.Background(), tc[0], nil)
		if err != nil || r.Kind != "array" || r.Copy != tc[1] {
			t.Fatal(tc[0], r, err)
		}
	}
}

func TestPairedStatisticsAndAdditionalFinanceKnownValues(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want float64
	}{
		{`=CORREL({1,2,3},{2,4,6})`, 1}, {`=PEARSON({1,2,3},{6,4,2})`, -1},
		{`=RSQ({2,4,6},{1,2,3})`, 1}, {`=SLOPE({3,5,7},{1,2,3})`, 2},
		{`=INTERCEPT({3,5,7},{1,2,3})`, 1}, {`=STEYX({3,5,7},{1,2,3})`, 0},
		{`=FORECAST(4,{3,5,7},{1,2,3})`, 9}, {`=FORECAST.LINEAR(0,{3,5,7},{1,2,3})`, 1},
		{`=COVARIANCE.P({1,2,3},{2,4,6})`, 4.0 / 3}, {`=COVAR({1,2,3},{2,4,6})`, 4.0 / 3},
		{`=COVARIANCE.S({1,2,3},{2,4,6})`, 2},
		{`=SUMX2MY2({1,2},{3,4})`, -20}, {`=SUMX2PY2({1,2},{3,4})`, 30}, {`=SUMXMY2({1,2},{3,4})`, 8},
		{`=SLOPE({3,"x",7},{1,99,3})`, 2},
		{`=EFFECT(0.12,12)`, 0.1268250301319698}, {`=NOMINAL(EFFECT(0.12,12),12)`, .12},
		{`=RRI(2,100,121)`, .1}, {`=PDURATION(0.1,100,121)`, 2},
		{`=MIRR({-100,0,121},0.1,0.1)`, .1}, {`=MIRR({-100,50,60},0.1,0.1)`, math.Sqrt(1.15) - 1},
		{`=DOLLARDE(1.02,16)`, 1.125}, {`=DOLLARFR(1.125,16)`, 1.02},
		{`=DOLLARDE(-1.02,16)`, -1.125}, {`=DOLLARFR(-1.125,16)`, -1.02},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			r, err := Formula(context.Background(), tc.expr, extendedResolver)
			if err != nil {
				t.Fatal(err)
			}
			n, err := Canonical(r.Value)
			if err != nil {
				t.Fatal(r, err)
			}
			got, _ := n.Float64()
			if math.Abs(got-tc.want) > 1e-11*math.Max(1, math.Abs(tc.want)) {
				t.Fatalf("%s got %v want %v", tc.expr, got, tc.want)
			}
		})
	}
	for _, expr := range []string{`=CORREL({1,1},{2,3})`, `=SLOPE({1,2},{1,1})`, `=STEYX({1,2},{1,2})`, `=COVARIANCE.S({1},{2})`, `=CORREL({1,2},{1})`, `=CORREL({1,#N/A},{2,3})`, `=EFFECT(0,12)`, `=NOMINAL(0.1,0)`, `=RRI(0,100,120)`, `=PDURATION(-0.1,100,120)`, `=MIRR({1,2},0.1,0.1)`, `=DOLLARFR(1,0)`} {
		if _, err := Formula(context.Background(), expr, nil); err == nil || !strings.HasPrefix(err.Error(), "#") {
			t.Fatal("invalid accepted", expr, err)
		}
	}
}
