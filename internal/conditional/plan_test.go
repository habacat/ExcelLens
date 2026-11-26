package conditional

import (
	"context"
	"math/big"
	"testing"

	"excellens/internal/formats"
)

func TestCostBasedStrategySelection(t *testing.T) {
	compile := func(formula string) *Expression[EvalValue] {
		t.Helper()
		expr, _, notice := CompileExpression(formula, "Sheet1", func(string) (Reference, string) {
			return Reference{}, "unknown"
		})
		if notice != "" || expr == nil {
			t.Fatalf("compile %q failed: %s", formula, notice)
		}
		// Unwrap comparison like SUM($C2:$C4)=6 -> SUM($C2:$C4).
		if expr.Kind == Binary {
			return expr.Args[0]
		}
		return expr
	}

	cases := []struct {
		name     string
		formula  string
		ranges   []Area
		anchorR  int
		want     Strategy
		wantDisk bool
	}{
		{
			name:     "short moving sum uses viewport cache",
			formula:  "SUM($C2:$C4)=6",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 1000000, EndCol: 6}},
			anchorR:  2,
			want:     StrategyViewportCache,
			wantDisk: false,
		},
		{
			name:     "short fixed sum uses viewport cache",
			formula:  "SUM($C$2:$C$10)=18",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 1000000, EndCol: 6}},
			anchorR:  2,
			want:     StrategyViewportCache,
			wantDisk: false,
		},
		{
			name:     "short fixed match uses viewport cache",
			formula:  "MATCH(0%,$A$1:$A$4,0)=1",
			ranges:   []Area{{Row: 1, Col: 2, EndRow: 100, EndCol: 2}},
			anchorR:  1,
			want:     StrategyViewportCache,
			wantDisk: false,
		},
		{
			name:     "medium moving sum uses sliding window",
			formula:  "SUM($C2:$C50)=98",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 1000000, EndCol: 6}},
			anchorR:  2,
			want:     StrategySlidingWindow,
			wantDisk: false,
		},
		{
			name:     "bounded running sum up to 100 rows uses sliding window",
			formula:  "SUM($C$2:$C2)>10",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 100, EndCol: 6}},
			anchorR:  2,
			want:     StrategySlidingWindow,
			wantDisk: false,
		},
		{
			name:     "large fixed sum uses shared aggregate",
			formula:  "SUM($C$2:$C$50000)>100",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 50000, EndCol: 6}},
			anchorR:  2,
			want:     StrategySharedAggregate,
			wantDisk: true,
		},
		{
			name:     "whole-column fixed sum uses shared aggregate",
			formula:  "SUM($C:$C)>100",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 50000, EndCol: 6}},
			anchorR:  2,
			want:     StrategySharedAggregate,
			wantDisk: true,
		},
		{
			name:     "large moving sum uses disk prefix",
			formula:  "SUM($C2:$C400)>100",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 1000000, EndCol: 6}},
			anchorR:  2,
			want:     StrategyDiskPrefix,
			wantDisk: true,
		},
		{
			name:     "large running sum uses disk prefix",
			formula:  "SUM($C$2:$C2)>100",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 100000, EndCol: 6}},
			anchorR:  2,
			want:     StrategyDiskPrefix,
			wantDisk: true,
		},
		{
			name:     "whole-column duplicate check uses disk group",
			formula:  "COUNTIF($A:$A,$A2)>1",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 1000000, EndCol: 6}},
			anchorR:  2,
			want:     StrategyDiskGroup,
			wantDisk: true,
		},
		{
			name:     "whole-column exact match uses disk match",
			formula:  "MATCH($A2,$A:$A,0)=ROW()",
			ranges:   []Area{{Row: 2, Col: 6, EndRow: 1000000, EndCol: 6}},
			anchorR:  2,
			want:     StrategyDiskMatch,
			wantDisk: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := compile(tc.formula)
			q, ok := AnalyzeStatQuery(call, tc.anchorR, 6, tc.ranges, tc.anchorR, 6)
			if !ok {
				t.Fatalf("AnalyzeStatQuery returned ok=false for %s", tc.formula)
			}
			if q.Plan.Strategy != tc.want {
				t.Fatalf("got strategy %s, want %s", q.Plan.Strategy, tc.want)
			}
			if q.Plan.Strategy.NeedsDiskIndex() != tc.wantDisk {
				t.Fatalf("NeedsDiskIndex=%v, want %v", q.Plan.Strategy.NeedsDiskIndex(), tc.wantDisk)
			}
			plans := CollectDiskPlans([]RulePlanInput{{
				AnchorRow: tc.anchorR,
				AnchorCol: 6,
				Ranges:    tc.ranges,
				Exprs:     []*Expression[EvalValue]{call},
			}})
			if tc.wantDisk && len(plans) != 1 {
				t.Fatalf("expected 1 disk plan, got %d", len(plans))
			}
			if !tc.wantDisk && len(plans) != 0 {
				t.Fatalf("expected 0 disk plans, got %+v", plans)
			}
		})
	}
}

func TestSlidingWindowSumIncrementalExactAndOrderedFallback(t *testing.T) {
	expr, _, notice := CompileExpression("SUM($C2:$C25)", "Sheet1", func(string) (Reference, string) {
		return Reference{}, "unknown"
	})
	if notice != "" {
		t.Fatal(notice)
	}
	ranges := []Area{{Row: 2, Col: 6, EndRow: 1000, EndCol: 6}}

	// Exact dyadic integer data: verify incremental reads (each row read once!).
	reads := 0
	eng := NewEngine(context.Background(), false)
	resolver := func(row, col int) (CellOperand, bool, error) {
		reads++
		return CellOperand{Raw: "2", Type: "n"}, true, nil
	}
	for row := 2; row <= 40; row++ {
		eng.ResetCell(row, 6, CellOperand{}, resolver)
		v, n, err := eng.Eval(expr, 2, 6, ranges)
		if err != nil || n != "" {
			t.Fatalf("row %d: err=%v notice=%q", row, err, n)
		}
		r, ok := ValueToRat(v)
		if !ok || r.Cmp(big.NewRat(48, 1)) != 0 {
			t.Fatalf("row %d: got %v, want 48", row, v)
		}
	}
	// Rows 2..63 = 62 unique rows read incrementally instead of 39 * 24 = 936!
	if reads != 62 {
		t.Fatalf("expected 62 incremental cell reads, got %d", reads)
	}

	// Floating-point cancellation (1e16 + 1 - 1e16 = 0 in IEEE754 left-to-right order).
	eng2 := NewEngine(context.Background(), false)
	values := map[int]CellOperand{
		2: {Raw: "10000000000000000", Type: "n"},
		3: {Raw: "1", Type: "n"},
		4: {Raw: "-10000000000000000", Type: "n"},
		5: {Raw: "5", Type: "n"},
	}
	resolver2 := func(row, col int) (CellOperand, bool, error) {
		return values[row], true, nil
	}
	eng2.ResetCell(2, 6, CellOperand{}, resolver2)
	v, n, err := eng2.Eval(expr, 2, 6, ranges)
	if err != nil || n != "" {
		t.Fatalf("cancellation eval failed: %v %q", err, n)
	}
	r, _ := ValueToRat(v)
	if r.Cmp(big.NewRat(5, 1)) != 0 {
		t.Fatalf("expected Excel left-to-right cancellation result 5, got %v", r)
	}
	// Slide by one row so 1e16 leaves and IEEE 754 left-to-right 1 + (-1e16) + 5 = -9999999999999996 remains.
	eng2.ResetCell(3, 6, CellOperand{}, resolver2)
	v, n, err = eng2.Eval(expr, 2, 6, ranges)
	if err != nil || n != "" {
		t.Fatalf("slid eval failed: %v %q", err, n)
	}
	r, _ = ValueToRat(v)
	wantFloat := float64(1) + float64(-10000000000000000) + float64(5)
	if r.Cmp(new(big.Rat).SetFloat64(wantFloat)) != 0 {
		t.Fatalf("expected IEEE 754 left-to-right result %.0f after sliding past row 2, got %v", wantFloat, r)
	}
}

func TestSlidingWindowRestoresIncrementalWorkAfterOutlierLeaves(t *testing.T) {
	for _, outlier := range []string{"0.1", "10000000000000000"} {
		engine := NewEngine(context.Background(), false)
		engine.Resolver = func(row, col int) (CellOperand, bool, error) {
			raw := "1"
			if row == 1 {
				raw = outlier
			}
			return CellOperand{Raw: raw, Type: "n"}, true, nil
		}
		expr := &Expression[EvalValue]{Kind: Call, Op: "SUM"}
		if _, notice, err, _ := engine.evalSlidingSum(expr, 2, 1, 24); notice != "" || err != nil {
			t.Fatal(notice, err)
		}
		engine.Work = 2
		v, notice, err, handled := engine.evalSlidingSum(expr, 2, 2, 25)
		if !handled || notice != "" || err != nil || FormatDecimalText(v) != "24" {
			t.Fatal(outlier, v, notice, err)
		}
	}
}

func TestMovingPrefixCostAtLastValidWorksheetRow(t *testing.T) {
	expr, _, why := CompileExpression("SUM($A$1:$A20)", "Sheet1", nil)
	if why != "" {
		t.Fatal(why)
	}
	q, ok := AnalyzeStatQuery(expr, 1, 2, []Area{{Row: 1, EndRow: formats.ExcelRows, Col: 2, EndCol: 2}}, 1, 2)
	if !ok || q.Plan.Strategy != StrategyDiskPrefix {
		t.Fatal(q.Plan.Strategy, q.WindowSpan)
	}
}
