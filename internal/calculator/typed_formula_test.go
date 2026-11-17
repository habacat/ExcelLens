package calculator

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
)

func TestTypedFormulaComparisonsTextAndLazyBranches(t *testing.T) {
	tests := []struct{ expression, want, kind string }{
		{"=1=1", "TRUE", "boolean"}, {"=1<>1", "FALSE", "boolean"}, {"=1<=2", "TRUE", "boolean"}, {"=3>2", "TRUE", "boolean"},
		{"=\"甲\"=\"甲\"", "TRUE", "boolean"}, {"=\"ABC\"=\"abc\"", "TRUE", "boolean"}, {"=\"001\"=\"1\"", "FALSE", "boolean"}, {"=\"1\"=1", "FALSE", "boolean"},
		{"=EXACT(\"ABC\",\"abc\")", "FALSE", "boolean"}, {"=\"凭证\"&\"001\"", "凭证001", "text"}, {"=CONCAT(\"合计：\",0.1+0.2)", "合计：0.3", "text"},
		{"=IF(1=1,\"一致\",\"不一致\")", "一致", "text"}, {"=IF(FALSE,1/0,\"正常\")", "正常", "text"}, {"=IFERROR(1/0,\"未匹配\")", "未匹配", "text"},
		{"=AND(1=1,2>1,NOT(FALSE))", "TRUE", "boolean"}, {"=OR(FALSE,TRUE())", "TRUE", "boolean"}, {"=ISERROR(1/0)", "TRUE", "boolean"},
		{"=LEN(\"王磊\")", "2", "number"}, {"=LEFT(\"000123\",3)", "000", "text"}, {"=RIGHT(\"北京分公司\",3)", "分公司", "text"}, {"=MID(\"北京分公司\",3,2)", "分公", "text"},
		{"=TRIM(\"  科目   明细  \")", "科目 明细", "text"}, {"=SUBSTITUTE(\"A-A-A\",\"A\",\"B\",2)", "A-B-A", "text"},
		{"=UPPER(\"abc\")", "ABC", "text"}, {"=LOWER(\"ABC\")", "abc", "text"}, {"=FIND(\"分\",\"北京分公司\")", "3", "number"},
		{"=SEARCH(\"bC\",\"aBcD\")", "2", "number"}, {"=VALUE(\"1,234.50\")+0.5", "1235", "number"},
		{"=TEXT(12,\"000000\")", "000012", "text"}, {"=TEXT(1234.5,\"#,##0.00\")", "1,234.50", "text"}, {"=TEXT(0.135,\"0.0%\")", "13.5%", "text"},
		{"=TEXTJOIN(\"/\",TRUE,\"北京\",\"\",\"分公司\")", "北京/分公司", "text"}, {"=\"（甲，乙）;1=1\"", "（甲，乙）;1=1", "text"},
		{"=\"a\"\"b\"", "a\"b", "text"}, {"=ISNUMBER(1)", "TRUE", "boolean"}, {"=ISTEXT(\"1\")", "TRUE", "boolean"},
		{"=SUM(\"1\",2)", "3", "number"}, {"=COUNT(1,\"abc\",2)", "2", "number"}, {"=COUNTA(1,\"abc\",2)", "3", "number"},
	}
	for _, tc := range tests {
		t.Run(tc.expression, func(t *testing.T) {
			r, err := Formula(context.Background(), tc.expression, nil)
			if err != nil || r.Display != tc.want || r.Kind != tc.kind {
				t.Fatalf("%+v %v; want %q (%s)", r, err, tc.want, tc.kind)
			}
		})
	}
}
func TestTypedReferenceValuesArePreservedAndNeverExecuted(t *testing.T) {
	calls := 0
	resolve := func(sheet, start, end string) (RangeValue, error) {
		calls++
		var scalar FormulaValue
		switch start {
		case "A1":
			scalar = TextValue("000123")
		case "A2":
			scalar = TextValue("=1+2")
		case "A3":
			scalar = BlankValue()
		case "A4":
			scalar = BoolValue(true)
		default:
			return RangeValue{}, errors.New("#REF! 不存在")
		}
		return RangeValue{Sum: new(big.Rat), Scalar: &scalar}, nil
	}
	for expr, want := range map[string]string{"=A1": "000123", "=LEFT(A1,3)": "000", "=A2": "=1+2", "=A3=0": "TRUE", "=A3=\"\"": "TRUE", "=ISBLANK(A3)": "TRUE", "=A4": "TRUE", "=A4+1": "2", "=A1=A1": "TRUE"} {
		r, err := Formula(context.Background(), expr, resolve)
		if err != nil || r.Display != want {
			t.Fatal(expr, r, err)
		}
	}
	before := calls
	r, err := Formula(context.Background(), "=IF(TRUE,\"无需读取\",Z99)", resolve)
	if err != nil || r.Display != "无需读取" || calls != before {
		t.Fatal("unused reference was evaluated", r, err)
	}
	if _, err = Formula(context.Background(), "=A2+1", resolve); err == nil {
		t.Fatal("source text executed")
	}
}
func TestTypedTextRangeAndResourceLimits(t *testing.T) {
	resolve := func(sheet, start, end string) (RangeValue, error) {
		return RangeValue{Sum: big.NewRat(3, 1), Count: 2, NonEmpty: 3, Min: big.NewRat(1, 1), Max: big.NewRat(2, 1), Load: func() ([]FormulaValue, error) {
			return []FormulaValue{TextValue("甲"), BlankValue(), TextValue("乙"), BoolValue(true)}, nil
		}}, nil
	}
	for expr, want := range map[string]string{"=TEXTJOIN(\"|\",FALSE,A1:B2)": "甲||乙|TRUE", "=TEXTJOIN(\"|\",TRUE,A1:B2)": "甲|乙|TRUE", "=COUNT(A1:B2)": "2", "=COUNTA(A1:B2)": "3", "=SUM(A1:B2)": "3"} {
		r, err := Formula(context.Background(), expr, resolve)
		if err != nil || r.Display != want {
			t.Fatal(expr, r, err)
		}
	}
	if _, err := Formula(context.Background(), "=\""+strings.Repeat("a", 4100)+"\"", nil); err == nil {
		t.Fatal("oversize expression accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Formula(ctx, "=IFERROR(1/0,\"x\")", nil); err == nil {
		t.Fatal("cancellation swallowed")
	}
	found := map[string]bool{}
	for _, f := range Functions() {
		if f.Name == "" || f.Signature == "" || found[f.Name] {
			t.Fatal("invalid function catalog", f)
		}
		found[f.Name] = true
	}
	for _, name := range []string{"IF", "SUM", "TEXT", "LEFT", "EXACT", "AND"} {
		if !found[name] {
			t.Fatal("missing completion", name)
		}
	}
}
