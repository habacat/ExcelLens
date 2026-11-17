package calculator

import (
	"context"
	"errors"
	"math/big"
	"testing"
)

func TestNativeArrayExpressionsAndFunctions(t *testing.T) {
	cases := [][2]string{
		{`=SUMPRODUCT((B1:B5="北京")*C1:C5)`, `10.1`}, {`=SUMPRODUCT((A1:A5>=2)*(B1:B5="北京*")*C1:C5)`, `0`}, {`=SUM(IF(B1:B5="北京",C1:C5,0))`, `10.1`}, {`=SUM(--(A1:A6>2))`, `4`}, {`=SUM(FILTER(C1:C5,B1:B5="北京"))`, `10.1`}, {`=SUM(SEQUENCE(3,2,1,1))`, `21`}, {`=SUM(TAKE(A1:C5,2,1))`, `3`}, {`=SUM(DROP(A1:A5,2))`, `12`}, {`=SUM(CHOOSECOLS(A1:C5,1))`, `15`}, {`=SUM(CHOOSEROWS(A1:A5,1,3,5))`, `9`}, {`=SUM(TRANSPOSE(A1:A5))`, `15`}, {`=SUM(HSTACK(A1:A5,A1:A5))`, `30`}, {`=SUM(VSTACK(A1:A5,A1:A5))`, `30`}, {`=SUM(TOCOL(A1:C5,2))`, `23.6`}, {`=SUM(TOROW(A1:C5,2))`, `23.6`}, {`=ROWS(UNIQUE(B1:B6))`, `5`}, {`=ROWS(UNIQUE(B1:B6,FALSE,TRUE))`, `4`}, {`=INDEX(SORT(A1:C5,1,-1),1,1)`, `5`}, {`=LET(amount,0.1+0.2,amount*10)`, `3`}, {`=LET(amount,A1:A5,SUM(amount))`, `15`}, {`=LET(amount,2,LET(amount,3,amount)+amount)`, `5`}, {`=SUM({1,2;3,4})`, `10`}, {`=SUMPRODUCT({1,2},{3,4})`, `11`}, {`=IF(FALSE,#N/A,"正常")`, `正常`}, {`=ISNA(#N/A)`, `TRUE`}, {`=TYPE(1/0)`, `16`}, {`=ISNUMBER(1/0)`, `FALSE`}, {`=ISTEXT(1/0)`, `FALSE`}, {`=ISBLANK(1/0)`, `FALSE`}, {`=ISLOGICAL(1/0)`, `FALSE`}, {`=ISNONTEXT(1/0)`, `TRUE`}, {`=ISREF(A1)`, `TRUE`},
	}
	for _, tc := range cases {
		t.Run(tc[0], func(t *testing.T) {
			r, err := Formula(context.Background(), tc[0], extendedResolver)
			if err != nil {
				t.Fatal(err)
			}
			if r.Kind == "number" {
				got, _ := Canonical(r.Value)
				want, err := Canonical(tc[1])
				if err != nil || got.Cmp(want) != 0 {
					t.Fatal(r, tc[1])
				}
			} else if r.Display != tc[1] {
				t.Fatal(r, tc[1])
			}
		})
	}
	arrays := [][2]string{{`=SEQUENCE(2,3)`, "1\t2\t3\n4\t5\t6"}, {`=TRANSPOSE({1,2;3,4})`, "1\t3\n2\t4"}, {`=CHOOSECOLS({1,2,3;4,5,6},3,1)`, "3\t1\n6\t4"}, {`=CHOOSEROWS({1,2,3;4,5,6},2,1)`, "4\t5\t6\n1\t2\t3"}, {`=FILTER({1,2,3;4,5,6},{TRUE,FALSE,TRUE})`, "1\t3\n4\t6"}, {`=SORT({3,1,2;6,4,5},1,1,TRUE)`, "1\t2\t3\n4\t5\t6"}, {`=UNIQUE({1,2,1;3,4,3},TRUE)`, "1\t2\n3\t4"}, {`=HSTACK({1;2},{3,4})`, "1\t3\t4\n2\t#N/A\t#N/A"}, {`=VSTACK({1,2},{3})`, "1\t2\n3\t#N/A"}, {`=TOCOL({1,2;3,4},0,TRUE)`, "1\n3\n2\n4"}, {`=TOROW({1,2;3,4},0,TRUE)`, "1\t3\t2\t4"}, {`=TEXTSPLIT("甲,乙;丙,丁",",",";")`, "甲\t乙\n丙\t丁"}, {`=TAKE({1,2;3,4},-1,-1)`, "4"}, {`=DROP({1,2;3,4},-1,-1)`, "1"}}
	for _, tc := range arrays {
		t.Run(tc[0], func(t *testing.T) {
			r, err := Formula(context.Background(), tc[0], extendedResolver)
			if err != nil || r.Kind != "array" || r.Copy != tc[1] {
				t.Fatalf("%+v %v want %q", r, err, tc[1])
			}
		})
	}
	for _, expr := range []string{`=SUM({1,2;3})`, `=FILTER(A1:A5,{TRUE,FALSE})`, `=TAKE(A1:A5,0)`, `=SEQUENCE(200001)`, `=SUMPRODUCT(A1:A5,C1:C6)`, `=SUMPRODUCT((A1:A6>2)*C1:C6)`, `=LET(A1,2,A1)`, `=LET(amount,2,3)+amount`} {
		if _, err := Formula(context.Background(), expr, extendedResolver); err == nil {
			t.Fatal("invalid accepted", expr)
		}
	}
}
func TestConditionalStreamingHasNoTextMaterializationLimit(t *testing.T) {
	const size = 150000
	visits := 0
	resolver := func(sheet, start, end string) (RangeValue, error) {
		r := RangeValue{Rows: size, Cols: 1}
		r.Load = func() ([]FormulaValue, error) { return nil, errors.New("text load must not run") }
		r.Visit = func(fn func(int, FormulaValue) error) error {
			visits++
			for i := 0; i < size; i++ {
				v := NumberValue(big.NewRat(1, 10))
				if start == "A1" {
					v = TextValue("机构甲")
					if i%2 == 1 {
						v = TextValue("机构乙")
					}
				}
				if err := fn(i, v); err != nil {
					return err
				}
			}
			return nil
		}
		return r, nil
	}
	r, err := Formula(context.Background(), `=SUMIF(A1:A150000,"机构甲",B1:B150000)`, resolver)
	if err != nil || r.Value != "7500" || visits != 2 {
		t.Fatal(r, err, visits)
	}
}
