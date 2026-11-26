package conditional

import (
	"fmt"
	"slices"
	"strconv"

	"excellens/internal/formats"
)

// Strategy selects the lowest-cost execution plan for a conditional range formula.
type Strategy uint8

const (
	StrategyViewportCache   Strategy = iota + 1 // Few cells: current viewport and bounded dependency cache
	StrategySlidingWindow                       // Short moving window: incremental sliding window without full-sheet disk index
	StrategySharedAggregate                     // Large fixed SUM: one shared aggregate record
	StrategyDiskPrefix                          // Large dynamic SUM: disk prefix sum index
	StrategyDiskGroup                           // Whole-column / large COUNTIF(S)/SUMIF(S): disk frequency/sum group index
	StrategyDiskMatch                           // Whole-column / large exact MATCH: disk first-match position index
)

func (s Strategy) String() string {
	switch s {
	case StrategyViewportCache:
		return "viewport-cache"
	case StrategySlidingWindow:
		return "sliding-window"
	case StrategySharedAggregate:
		return "shared-aggregate"
	case StrategyDiskPrefix:
		return "disk-prefix"
	case StrategyDiskGroup:
		return "disk-group"
	case StrategyDiskMatch:
		return "disk-match"
	default:
		return "none"
	}
}

func (s Strategy) NeedsDiskIndex() bool {
	switch s {
	case StrategySharedAggregate, StrategyDiskPrefix, StrategyDiskGroup, StrategyDiskMatch:
		return true
	default:
		return false
	}
}

// StatPlan describes one worksheet-scoped disk statistics plan or in-memory cost decision.
type StatPlan struct {
	Strategy  Strategy
	Kind      string // "group", "prefix", "match" for disk buckets
	Cols      []int
	From, To  int
	SumCol    int
	Constants []EvalValue
	Literals  []string
}

func (p StatPlan) Key() string {
	return fmt.Sprintf("%s/%d/%d/%d/%v/%q", p.Kind, p.From, p.To, p.SumCol, p.Cols, p.Literals)
}

func (p StatPlan) Equal(other StatPlan) bool {
	return p.Strategy == other.Strategy &&
		p.Kind == other.Kind &&
		p.From == other.From &&
		p.To == other.To &&
		p.SumCol == other.SumCol &&
		slices.Equal(p.Cols, other.Cols) &&
		slices.Equal(p.Literals, other.Literals)
}

// StatQuery binds an AST call node to its chosen strategy and resolved bounds.
type StatQuery struct {
	Plan       StatPlan
	Terms      []*Expression[EvalValue]
	Lo, Hi     int
	WindowSpan int
}

// AnalyzeStatQuery inspects a conditional function call and chooses the lowest-cost
// execution strategy based on range geometry and rule coverage.
func AnalyzeStatQuery(n *Expression[EvalValue], anchorRow, anchorCol int, groupRanges []Area, evalRow, evalCol int) (q StatQuery, ok bool) {
	if n == nil || n.Kind != Call {
		return q, false
	}
	args := n.Args
	bounds := func(a *Expression[EvalValue], fixed bool) (lo, hi, c int, valid bool) {
		if (a.Kind != Range && a.Kind != ReferenceCell) || a.Ref.ForeignSheet != "" {
			return 0, 0, 0, false
		}
		if fixed && a.Ref.Row != 0 && (!a.Ref.AbsoluteRow || (a.Kind == Range && !a.EndRef.AbsoluteRow)) {
			return 0, 0, 0, false
		}
		r1, c1, r2, c2, resolved := ResolveRangeBounds(a, evalRow, evalCol, anchorRow, anchorCol)
		return r1, r2, c1, resolved && c1 == c2
	}
	p := &q.Plan
	switch n.Op {
	case "SUM":
		if len(args) != 1 || args[0].Kind != Range {
			return q, false
		}
		var valid bool
		q.Lo, q.Hi, p.SumCol, valid = bounds(args[0], false)
		if !valid {
			return q, false
		}
		if _, _, _, fixed := bounds(args[0], true); fixed {
			span := q.Hi - q.Lo + 1
			q.WindowSpan = span
			p.From, p.To = q.Lo, q.Hi
			if span <= formats.ConditionalStatsDirectRangeRows {
				p.Strategy = StrategyViewportCache
				return q, true
			}
			p.Strategy = StrategySharedAggregate
			p.Kind = "group"
			return q, true
		}
		maxSpan := movingSumMaxSpan(args[0], anchorRow, groupRanges)
		q.WindowSpan = maxSpan
		if maxSpan <= formats.ConditionalStatsDirectRangeRows {
			p.Strategy = StrategyViewportCache
			return q, true
		}
		if maxSpan <= formats.ConditionalStatsSlidingWindowRows {
			p.Strategy = StrategySlidingWindow
			return q, true
		}
		p.Strategy = StrategyDiskPrefix
		p.Kind = "prefix"
		p.From, p.To = 1, formats.ExcelRows
		return q, true

	case "MATCH":
		if len(args) != 3 || args[2].Kind != Literal || args[2].Value.Kind != "number" || IsNonZero(args[2].Value) {
			return q, false
		}
		lo, hi, c, valid := bounds(args[1], true)
		if !valid {
			return q, false
		}
		q.Lo, q.Hi = lo, hi
		span := hi - lo + 1
		q.WindowSpan = span
		p.From, p.To, p.Cols = lo, hi, []int{c}
		q.Terms = []*Expression[EvalValue]{args[0]}
		if span <= formats.ConditionalStatsDirectRangeRows {
			p.Strategy = StrategyViewportCache
			return q, true
		}
		p.Strategy = StrategyDiskMatch
		p.Kind = "match"
		return q, true

	case "COUNTIF", "COUNTIFS", "SUMIF", "SUMIFS":
	default:
		return q, false
	}

	start, end := 0, len(args)
	if n.Op == "SUMIFS" {
		start = 1
	}
	if n.Op == "SUMIF" {
		end = 2
	}
	if (end-start)/2 > formats.ConditionalStatsColumns {
		return q, false
	}
	for i := start; i < end; i += 2 {
		lo, hi, c, valid := bounds(args[i], true)
		if !valid {
			return q, false
		}
		if i == start {
			p.From, p.To = lo, hi
		} else if lo != p.From || hi != p.To {
			return q, false
		}
		p.Cols = append(p.Cols, c)
		term := args[i+1]
		q.Terms = append(q.Terms, term)
		literal := ""
		if term.Kind == Literal {
			// Literal comparisons (including inequalities and wildcards) can be
			// applied once while building the index, independent of the target row.
			literal = term.Value.Kind + ":" + term.Value.Text
			if term.Value.Kind == "number" {
				literal = "n:" + FormatDecimalText(term.Value)
			}
			if term.Value.Kind == "boolean" {
				literal = "b:" + strconv.FormatBool(term.Value.Boolean)
			}
		}
		p.Constants = append(p.Constants, term.Value)
		p.Literals = append(p.Literals, literal)
	}
	if n.Op == "SUMIF" {
		p.SumCol = p.Cols[0]
		if len(args) == 3 {
			r, c, _, _, valid := ResolveRangeBounds(args[2], evalRow, evalCol, anchorRow, anchorCol)
			if !valid || r != p.From || args[2].Ref.ForeignSheet != "" || (args[2].Ref.Row != 0 && !args[2].Ref.AbsoluteRow) {
				return q, false
			}
			p.SumCol = c
		}
	}
	if n.Op == "SUMIFS" {
		lo, hi, c, valid := bounds(args[0], true)
		if !valid || lo != p.From || hi != p.To {
			return q, false
		}
		p.SumCol = c
	}
	q.Lo, q.Hi = p.From, p.To
	span := p.To - p.From + 1
	q.WindowSpan = span
	if span <= formats.ConditionalStatsDirectRangeRows {
		p.Strategy = StrategyViewportCache
		return q, true
	}
	p.Strategy = StrategyDiskGroup
	p.Kind = "group"
	return q, true
}

// movingSumMaxSpan computes the worst-case vertical window height for a dynamic
// single-column SUM range across all rule areas.
func movingSumMaxSpan(arg *Expression[EvalValue], anchorRow int, groupRanges []Area) int {
	a, b := arg.Ref, arg.EndRef
	if a.Row == 0 || b.Row == 0 {
		return formats.ExcelRows
	}
	// Both endpoints relative: constant-width sliding window independent of row.
	if !a.AbsoluteRow && !b.AbsoluteRow {
		diff := b.Row - a.Row
		if diff < 0 {
			diff = -diff
		}
		return diff + 1
	}
	// One endpoint absolute, one relative (e.g., running sum $C$2:$C2):
	// evaluate the span at every area's top and bottom row boundary.
	if len(groupRanges) == 0 {
		return formats.ExcelRows
	}
	offset, fixed := b.Row-anchorRow, a.Row
	if !a.AbsoluteRow {
		offset, fixed = a.Row-anchorRow, b.Row
	}
	maxSpan := 1
	for _, area := range groupRanges {
		// A relative endpoint can cross the sheet edge partway through an area.
		// The last valid cell still governs the cost of the preceding prefix.
		first := max(area.Row, 1-offset)
		last := min(area.EndRow, formats.ExcelRows-offset)
		if first <= last {
			maxSpan = max(maxSpan, fixed-(first+offset)+1, last+offset-fixed+1)
		}
	}
	return maxSpan
}

// RulePlanInput passes compiled rule expressions into CollectDiskPlans without
// coupling internal/conditional to workbook's internal rule structures.
type RulePlanInput struct {
	AnchorRow, AnchorCol int
	Ranges               []Area
	Exprs                []*Expression[EvalValue]
}

// CollectDiskPlans extracts the deduplicated set of plans whose cost warrants
// building a worksheet-scoped disk statistics index.
func CollectDiskPlans(rules []RulePlanInput) []StatPlan {
	var plans []StatPlan
	seen := map[string]bool{}
	for _, rule := range rules {
		var walk func(*Expression[EvalValue])
		walk = func(n *Expression[EvalValue]) {
			if n == nil || len(plans) >= formats.ConditionalStatsPlans {
				return
			}
			if n.Kind == Call {
				if q, ok := AnalyzeStatQuery(n, rule.AnchorRow, rule.AnchorCol, rule.Ranges, rule.AnchorRow, rule.AnchorCol); ok && q.Plan.Strategy.NeedsDiskIndex() {
					key := q.Plan.Key()
					if !seen[key] {
						seen[key] = true
						plans = append(plans, q.Plan)
					}
				}
			}
			for _, arg := range n.Args {
				walk(arg)
			}
		}
		for _, expr := range rule.Exprs {
			walk(expr)
		}
	}
	return plans
}
