package calculator

import (
	"context"
	"math/big"
	"strings"
	"testing"
)

func TestFinancialExactArithmeticPrecedenceAndRounding(t *testing.T) {
	for _, tc := range []struct{ expr, want string }{
		{"0.1+0.2", "0.3"}, {"9007199254740993+1", "9007199254740994"},
		{"123456789012345.999999-123456789012345.999998", "0.000001"},
		{"2+3*4", "14"}, {"(2+3)*4", "20"}, {"100*(1+13%)", "113"}, {"100+10%", "100.1"},
		{"ROUND(1.005,2)", "1.01"}, {"ROUND(-1.005,2)", "-1.01"}, {"ROUND(1250,-2)", "1300"},
		{"ROUNDDOWN(-1.239,2)", "-1.23"}, {"ROUNDUP(-1.231,2)", "-1.24"},
		{"ABS(-12.50)", "12.5"}, {"SUM(1,2,3*4)", "15"}, {"AVG(1,2,3)", "2"},
		{"MAX(-1,-2,-3)", "-1"}, {"MIN(1,2,3)", "1"}, {"2^3^2", "64"}, {"-2^2", "4"}, {"2^-2", "0.25"},
		{"1/3*3", "1"}, {"（1＋2）", "3"},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			r, err := Evaluate(context.Background(), Request{Expression: tc.expr, Precision: -1})
			if tc.want == "" {
				if err == nil {
					t.Fatal("unsupported expression accepted")
				}
				return
			}
			if err != nil || r.Display != tc.want {
				t.Fatalf("%s => %+v / %v, want %s", tc.expr, r, err, tc.want)
			}
		})
	}
}

func TestReferencesAreSnapshotsAndReuseUnroundedResults(t *testing.T) {
	req := Request{Expression: "[2]-[1]+[2]", Operands: map[string]string{"1": "0.1", "2": "0.2"}, Precision: 2}
	r, err := Evaluate(context.Background(), req)
	if err != nil || r.Copy != "0.30" || r.Exact != "0.3" || strings.Join(r.Used, ",") != "2,1" {
		t.Fatal(r, err)
	}
	r, err = Evaluate(context.Background(), Request{Expression: "1/3", Precision: 2})
	if err != nil || r.Copy != "0.33" || r.Value != "1/3" || !r.Rounded {
		t.Fatal(r, err)
	}
	ans, err := Evaluate(context.Background(), Request{Expression: "[1]*3", Operands: map[string]string{"1": r.Value}, Precision: -1})
	if err != nil || ans.Copy != "1" {
		t.Fatal("rounded value leaked into another calculation", ans, err)
	}
	zero, err := Evaluate(context.Background(), Request{Expression: "0.000001", Precision: 2})
	if err != nil || zero.Zero || !zero.Rounded || zero.Copy != "0.00" {
		t.Fatal("display rounding falsely marked reconciliation", zero, err)
	}
	percent, err := Evaluate(context.Background(), Request{Expression: "105/100-1", Precision: 2, Percent: true})
	if err != nil || percent.Copy != "5.00%" || percent.Exact != "0.05" {
		t.Fatal(percent, err)
	}
	manual := map[string]string{"1": "7.28", "2": "6636.46"}
	_, _ = Evaluate(context.Background(), Request{Expression: "[1]+[2]", Operands: manual, Precision: -1})
	if manual["1"] != "7.28" {
		t.Fatal("operand was mutated")
	}
}

func TestSafeGrammarErrorsCancellationAndResourceLimits(t *testing.T) {
	for _, expr := range []string{"1/0", "0^-1", "1+", "(1+2", "SUM()", "eval(1)", "fetch(1)", "[1]+1", "2(3)", "2^100000", "1,234.56", "1e999999", strings.Repeat("(", 50) + "1" + strings.Repeat(")", 50), "1" + strings.Repeat("%", 600)} {
		if r, err := Evaluate(context.Background(), Request{Expression: expr, Precision: -1}); err == nil {
			t.Fatalf("invalid %q accepted: %+v", expr, r)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Evaluate(ctx, Request{Expression: "1+2", Precision: -1}); err == nil {
		t.Fatal("cancelled work proceeded")
	}
	if _, err := Evaluate(context.Background(), Request{Expression: "1", Precision: 21}); err == nil {
		t.Fatal("unbounded precision accepted")
	}
	if _, err := Canonical("1/0"); err == nil {
		t.Fatal("zero denominator accepted")
	}
	v := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Mul(big.NewInt(3), new(big.Int).Exp(big.NewInt(10), big.NewInt(400), nil)))
	if result := Format(v, -1, false); !result.Approximate || result.Copy == "0" || result.Zero {
		t.Fatal("tiny nonzero value vanished", result)
	}
}

func BenchmarkAuditReconciliation(b *testing.B) {
	req := Request{Expression: "([1]+[2]-[3])*1.13-ROUND([4]/3,2)", Operands: map[string]string{"1": "123456789012345.999999", "2": "0.1", "3": "123456789012345.999998", "4": "6636.46"}, Precision: 6}
	for b.Loop() {
		if _, err := Evaluate(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}
