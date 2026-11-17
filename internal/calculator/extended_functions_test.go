package calculator

import (
	"context"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
)

func extendedResolver(sheet, start, end string) (RangeValue, error) {
	c1, r1 := formulaCoordinates(start)
	c2, r2 := formulaCoordinates(end)
	if end == "" {
		c2, r2 = c1, r1
	}
	if c1 == 0 || r1 == 0 || c2 == 0 || r2 == 0 {
		return RangeValue{}, errors.New("#REF! test reference")
	}
	if c1 > c2 {
		c1, c2 = c2, c1
	}
	if r1 > r2 {
		r1, r2 = r2, r1
	}
	rows, cols := r2-r1+1, c2-c1+1
	values := make([]FormulaValue, rows*cols)
	labels := []string{"北京", "上海", "北京", "北京东", "", "A*B"}
	amounts := []string{"0.1", "0.2", "10", "-2", "0.3"}
	for r := r1; r <= r2; r++ {
		for c := c1; c <= c2; c++ {
			v := BlankValue()
			if r >= 1 && r <= 6 {
				switch c {
				case 1:
					v = NumberValue(big.NewRat(int64(r), 1))
				case 2:
					if labels[r-1] != "" {
						v = TextValue(labels[r-1])
					}
				case 3:
					if r <= 5 {
						n, _ := Canonical(amounts[r-1])
						v = NumberValue(n)
					} else {
						v = errorValue(errors.New("#DIV/0!"))
					}
				case 4:
					if r == 1 {
						v = NumberValue(big.NewRat(-100, 1))
					} else if r == 2 {
						v = NumberValue(big.NewRat(110, 1))
					}
				}
			}
			values[(r-r1)*cols+c-c1] = v
		}
	}
	result := RangeValue{Rows: rows, Cols: cols, FirstRow: r1, FirstCol: c1, Sum: new(big.Rat)}
	for _, v := range values {
		if v.Kind != "blank" {
			result.NonEmpty++
		}
		if v.Kind == "error" {
			result.Fault = errors.New(v.Text)
		}
		if number, ok := dataNumber(v); ok {
			result.Count++
			result.Sum.Add(result.Sum, number)
			if result.Min == nil || number.Cmp(result.Min) < 0 {
				result.Min = new(big.Rat).Set(number)
			}
			if result.Max == nil || number.Cmp(result.Max) > 0 {
				result.Max = new(big.Rat).Set(number)
			}
		}
	}
	if end == "" {
		result.Scalar = &values[0]
	}
	result.Load = func() ([]FormulaValue, error) { return values, nil }
	result.Visit = func(fn func(int, FormulaValue) error) error {
		for i, v := range values {
			if v.Kind != "blank" {
				if err := fn(i, v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	result.At = func(i int) (FormulaValue, error) { v := values[i]; return v, valueFault(v) }
	return result, nil
}
func TestExtendedExcelFunctionsKnownResults(t *testing.T) {
	cases := [][2]string{
		{`=SUMIF(B1:B6,"北京",C1:C6)`, `10.1`}, {`=SUMIFS(C1:C6,A1:A6,">=2",B1:B6,"北京*")`, `8`}, {`=COUNTIF(B1:B6,"北京*")`, `3`}, {`=COUNTIF(B1:B6,"A~*B")`, `1`}, {`=COUNTIF(A1:A6,">"&3)`, `3`}, {`=COUNTIFS(A1:A6,">1",B1:B6,"北京*")`, `2`}, {`=AVERAGEIF(B1:B6,"北京",C1:C6)`, `5.05`}, {`=AVERAGEIFS(C1:C6,B1:B6,"北京*",A1:A6,">1")`, `4`}, {`=MINIFS(C1:C6,B1:B6,"北京*")`, `-2`}, {`=MAXIFS(C1:C6,B1:B6,"北京*")`, `10`}, {`=COUNTBLANK(B1:B6)`, `1`},
		{`=SUMPRODUCT(A1:A5,C1:C5)`, `24`}, {`=PRODUCT(A1:A6)`, `720`}, {`=SUMSQ(A1:A6)`, `91`}, {`=MEDIAN(A1:A6)`, `3.5`}, {`=LARGE(A1:A6,2)`, `5`}, {`=SMALL(A1:A6,2)`, `2`}, {`=RANK(3,A1:A6)`, `4`}, {`=RANK.EQ(3,A1:A6,1)`, `3`}, {`=RANK.AVG(3,A1:A6)`, `4`}, {`=PERCENTILE.INC(A1:A6,0.25)`, `2.25`}, {`=PERCENTILE.EXC(A1:A6,0.25)`, `1.75`}, {`=QUARTILE(A1:A6,1)`, `2.25`}, {`=QUARTILE.INC(A1:A6,1)`, `2.25`}, {`=QUARTILE.EXC(A1:A6,1)`, `1.75`}, {`=VAR.S(A1:A6)`, `3.5`}, {`=VAR.P(A1:A6)`, `35/12`}, {`=DEVSQ(A1:A6)`, `17.5`}, {`=AVEDEV(A1:A6)`, `1.5`},
		{`=INDEX(A1:C6,3,2)`, `北京`}, {`=SUM(INDEX(A1:C6,0,1))`, `21`}, {`=SUM(OFFSET(A1,1,0,3,1))`, `9`}, {`=OFFSET(A1,1,2)`, `0.2`}, {`=MATCH("北京*",B1:B6,0)`, `1`}, {`=MATCH(3.5,A1:A6,1)`, `3`}, {`=XMATCH("北京",B1:B6,0,-1)`, `3`}, {`=XLOOKUP(2,A1:A6,B1:B6)`, `上海`}, {`=XLOOKUP(9,A1:A6,B1:B6,"没有")`, `没有`}, {`=VLOOKUP(4,A1:C6,3,FALSE)`, `-2`}, {`=VLOOKUP(3.5,A1:C6,3,TRUE)`, `10`}, {`=HLOOKUP("北京",A1:C3,3,FALSE)`, `北京`}, {`=LOOKUP(3.5,A1:A6,C1:C6)`, `10`}, {`=ROWS(A1:C6)`, `6`}, {`=COLUMNS(A1:C6)`, `3`}, {`=ROW(A3)`, `3`}, {`=COLUMN(C2)`, `3`}, {`=ADDRESS(3,2)`, `$B$3`}, {`=ADDRESS(3,2,4)`, `B3`}, {`=INDIRECT("A3")`, `3`}, {`=_xlfn.XLOOKUP(2,A1:A6,B1:B6)`, `上海`},
		{`=SIGN(-8)`, `-1`}, {`=INT(-2.3)`, `-3`}, {`=TRUNC(-2.345,2)`, `-2.34`}, {`=TRUNC(123.45,-1)`, `120`}, {`=MOD(-3,2)`, `1`}, {`=MOD(3,-2)`, `-1`}, {`=QUOTIENT(-5,2)`, `-2`}, {`=POWER(2,10)`, `1024`}, {`=SQRT(2.25)`, `1.5`}, {`=FACT(5)`, `120`}, {`=FACTDOUBLE(7)`, `105`}, {`=COMBIN(6,2)`, `15`}, {`=COMBINA(3,2)`, `6`}, {`=PERMUT(6,2)`, `30`}, {`=PERMUTATIONA(3,2)`, `9`}, {`=GCD(24,18)`, `6`}, {`=LCM(4,6)`, `12`}, {`=EVEN(-3)`, `-4`}, {`=ODD(0)`, `1`}, {`=MROUND(2.5,1)`, `3`}, {`=CEILING(-4.3,2)`, `-4`}, {`=FLOOR(-4.3,2)`, `-6`}, {`=CEILING.MATH(-4.3,2,1)`, `-6`}, {`=FLOOR.MATH(-4.3,2,1)`, `-4`}, {`=CEILING.PRECISE(-4.3,2)`, `-4`}, {`=FLOOR.PRECISE(-4.3,2)`, `-6`},
		{`=IFS(FALSE,1,TRUE,"符合")`, `符合`}, {`=SWITCH(2,1,"甲",2,"乙","默认")`, `乙`}, {`=CHOOSE(2,"甲","乙")`, `乙`}, {`=IFNA(NA(),"未找到")`, `未找到`}, {`=ISNA(NA())`, `TRUE`}, {`=ISERR(1/0)`, `TRUE`}, {`=ISLOGICAL(TRUE)`, `TRUE`}, {`=ISNONTEXT(3)`, `TRUE`}, {`=ISEVEN(-4.9)`, `TRUE`}, {`=ISODD(3)`, `TRUE`}, {`=N(TRUE)`, `1`}, {`=T(3)`, ``}, {`=TYPE(TRUE)`, `4`}, {`=ERROR.TYPE(1/0)`, `2`}, {`=XOR(TRUE,TRUE,FALSE)`, `FALSE`},
		{`=PROPER("hELLO worLD")`, `Hello World`}, {`=CLEAN("a"&CHAR(10)&"b")`, `ab`}, {`=CODE("A")`, `65`}, {`=CHAR(65)`, `A`}, {`=UNICODE("中")`, `20013`}, {`=UNICHAR(20013)`, `中`}, {`=REPLACE("000123",2,2,"X")`, `0X123`}, {`=REPT("审",3)`, `审审审`}, {`=NUMBERVALUE("1.234,50",",",".")`, `1234.5`}, {`=FIXED(1234.567,2)`, `1,234.57`}, {`=TEXTBEFORE("a/b/c","/",-1)`, `a/b`}, {`=TEXTAFTER("a/b/c","/",-1)`, `c`},
		{`=DATE(2024,1,1)`, `45292`}, {`=YEAR(45292)`, `2024`}, {`=MONTH(45292)`, `1`}, {`=DAY(45292)`, `1`}, {`=DAY(60)`, `29`}, {`=DATE(1900,2,29)`, `60`}, {`=HOUR(TIME(15,30,45))`, `15`}, {`=MINUTE(TIME(15,30,45))`, `30`}, {`=SECOND(TIME(15,30,45))`, `45`}, {`=DATEVALUE("2024-01-01")`, `45292`}, {`=TIMEVALUE("12:00")`, `0.5`}, {`=WEEKDAY(DATE(2024,1,1),2)`, `1`}, {`=WEEKNUM(DATE(2024,1,1),21)`, `1`}, {`=ISOWEEKNUM(DATE(2024,1,1))`, `1`}, {`=EDATE(DATE(2024,1,31),1)`, `45351`}, {`=EOMONTH(DATE(2024,2,1),0)`, `45351`}, {`=DAYS(DATE(2024,1,2),DATE(2024,1,1))`, `1`}, {`=DATEDIF(DATE(2020,2,1),DATE(2024,1,31),"M")`, `47`}, {`=DAYS360(DATE(2024,1,1),DATE(2025,1,1))`, `360`}, {`=YEARFRAC(DATE(2024,1,1),DATE(2025,1,1),3)`, `366/365`}, {`=NETWORKDAYS(DATE(2024,1,1),DATE(2024,1,7))`, `5`}, {`=NETWORKDAYS.INTL(DATE(2024,1,1),DATE(2024,1,7),11)`, `6`}, {`=WORKDAY(DATE(2024,1,5),1)`, `45299`}, {`=WORKDAY.INTL(DATE(2024,1,5),1,11)`, `45297`}, {`=TEXT(DATE(2024,2,29),"yyyy-mm-dd")`, `2024-02-29`}, {`=TEXT(TIME(15,30,45),"hh:mm:ss")`, `15:30:45`},
	}
	for _, tc := range cases {
		t.Run(tc[0], func(t *testing.T) {
			r, err := Formula(context.Background(), tc[0], extendedResolver)
			if err != nil {
				t.Fatal(err)
			}
			if r.Kind == "number" {
				want, err := Canonical(tc[1])
				if err != nil {
					t.Fatal(err)
				}
				got, err := Canonical(r.Value)
				if err != nil || got.Cmp(want) != 0 {
					t.Fatalf("got %s want %s", r.Value, tc[1])
				}
			} else if r.Display != tc[1] {
				t.Fatalf("got %q want %q", r.Display, tc[1])
			}
		})
	}
	t.Logf("%d native function names registered; %d known-result cases", len(Functions()), len(cases))
}
func TestApproximateFunctionsHaveFiniteKnownValuesAndExplicitMarker(t *testing.T) {
	cases := []struct {
		expression string
		want       float64
	}{
		{`=SQRT(2)`, math.Sqrt2}, {`=PI()`, math.Pi}, {`=EXP(1)`, math.E}, {`=LN(EXP(1))`, 1}, {`=LOG(8,2)`, 3}, {`=LOG10(100)`, 2}, {`=SIN(0)`, 0}, {`=COS(0)`, 1}, {`=TAN(0)`, 0}, {`=ASIN(0)`, 0}, {`=ACOS(1)`, 0}, {`=ATAN(1)`, math.Pi / 4}, {`=ATAN2(1,1)`, math.Pi / 4}, {`=SINH(0)`, 0}, {`=COSH(0)`, 1}, {`=TANH(0)`, 0}, {`=ASINH(0)`, 0}, {`=ACOSH(1)`, 0}, {`=ATANH(0)`, 0}, {`=DEGREES(PI())`, 180}, {`=RADIANS(180)`, math.Pi}, {`=SQRTPI(1)`, math.Sqrt(math.Pi)}, {`=STDEV.S(A1:A6)`, math.Sqrt(3.5)}, {`=STDEV.P(A1:A6)`, math.Sqrt(35.0 / 12)}, {`=GEOMEAN(A1:A6)`, math.Pow(720, 1.0/6)}, {`=HARMEAN(1,2,3)`, 18.0 / 11}, {`=NORM.DIST(0,0,1,TRUE)`, .5}, {`=NORM.INV(0.5,10,2)`, 10}, {`=NORM.S.DIST(0,FALSE)`, 1 / math.Sqrt(2*math.Pi)}, {`=NORM.S.INV(0.5)`, 0},
		{`=PV(0.05,10,-100)`, 772.173492918482}, {`=FV(0.05,10,-100)`, 1257.78925355488}, {`=PMT(0.1,2,100)`, -57.6190476190476}, {`=NPER(0,-100,1000)`, 10}, {`=RATE(2,-57.6190476190476,100)`, .1}, {`=NPV(0.1,D1:D2)`, 0}, {`=IRR(D1:D2)`, .1}, {`=IPMT(0.1,1,2,100)`, -10}, {`=PPMT(0.1,1,2,100)`, -47.6190476190476}, {`=SLN(100,10,3)`, 30}, {`=SYD(100,10,3,1)`, 45}, {`=DDB(100,10,5,1)`, 40},
	}
	for _, tc := range cases {
		t.Run(tc.expression, func(t *testing.T) {
			r, err := Formula(context.Background(), tc.expression, extendedResolver)
			if err != nil {
				t.Fatal(err)
			}
			n, err := Canonical(r.Value)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := n.Float64()
			if math.Abs(got-tc.want) > 1e-10*math.Max(1, math.Abs(tc.want)) || !r.Approximate {
				t.Fatalf("%+v got %v want %v", r, got, tc.want)
			}
		})
	}
}
func TestExtendedErrorCasesAndLazyBranches(t *testing.T) {
	for _, expr := range []string{`=SUMIF(B1:B6,"A~*B",C1:C6)`, `=SUMIFS(C1:C5,A1:A6,">0")`, `=COUNTIFS(A1:A6,">0",B1:B5,"x")`, `=INDEX(A1:C6,9,1)`, `=VLOOKUP(1,A1:C6,4,FALSE)`, `=MOD(1,0)`, `=SQRT(-1)`, `=FACT(1001)`, `=IFNA(1/0,"不能吞掉")`, `=WORKDAY(1,1,"错误日期")`, `=NETWORKDAYS.INTL(1,2,"1111111")`, `=IRR(A1:A6)`} {
		if _, err := Formula(context.Background(), expr, extendedResolver); err == nil {
			t.Fatal("accepted invalid expression", expr)
		}
	}
	for expr, want := range map[string]string{`=IFS(TRUE,"正常",TRUE,1/0)`: "正常", `=CHOOSE(2,1/0,"正常")`: "正常", `=XLOOKUP(2,A1:A6,B1:B6,1/0)`: "上海"} {
		r, err := Formula(context.Background(), expr, extendedResolver)
		if err != nil || r.Display != want {
			t.Fatal(expr, r, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Formula(ctx, `=COUNTIF(A1:A6,">0")`, extendedResolver); err == nil {
		t.Fatal("cancel swallowed")
	}
	r, err := Formula(WithDateSystem(context.Background(), true), `=DATE(1904,1,1)`, nil)
	if err != nil || r.Value != "0" {
		t.Fatal(r, err)
	}
	names := map[string]bool{}
	for _, f := range Functions() {
		if f.Name == "" || names[f.Name] || !strings.HasPrefix(f.Signature, f.Name+"(") {
			t.Fatal(f)
		}
		names[f.Name] = true
	}
	if len(names) < 180 {
		t.Fatal("catalog too narrow", len(names))
	}
}
