package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"excellens/internal/conditional"
	"excellens/internal/formats"
)

type conditionalStatPlan struct {
	kind             string
	cols             []int
	from, to, sumCol int
	constants        []conditionalValue
	literals         []string
}

func (p conditionalStatPlan) key() string {
	return fmt.Sprintf("%s/%d/%d/%d/%v/%q", p.kind, p.from, p.to, p.sumCol, p.cols, p.literals)
}

type conditionalStatQuery struct {
	plan   conditionalStatPlan
	terms  []*conditionalExpr
	lo, hi int
}

func (e *ConditionalEvaluator) statQuery(n *conditionalExpr, g *conditionalGroup) (q conditionalStatQuery, ok bool) {
	args := n.Args
	bounds := func(a *conditionalExpr, fixed bool) (lo, hi, c int, ok bool) {
		if a.Kind != cfExprRange && a.Kind != cfExprRef || a.Ref.ForeignSheet != "" {
			return
		}
		if fixed && a.Ref.Row != 0 && (!a.Ref.AbsoluteRow || a.Kind == cfExprRange && !a.EndRef.AbsoluteRow) {
			return
		}
		r1, c1, r2, c2, valid := e.resolveRangeBounds(a, g)
		return r1, r2, c1, valid && c1 == c2
	}
	p := &q.plan
	switch n.Op {
	case "SUM":
		if len(args) != 1 || args[0].Kind != cfExprRange {
			return
		}
		var valid bool
		q.lo, q.hi, p.sumCol, valid = bounds(args[0], false)
		if !valid {
			return
		}
		if _, _, _, fixed := bounds(args[0], true); fixed {
			p.kind = "group"
			p.from, p.to = q.lo, q.hi
			return q, true
		}
		p.kind = "prefix"
		p.from, p.to = 1, formats.ExcelRows
		return q, true
	case "MATCH":
		if len(args) != 3 || args[2].Kind != cfExprLiteral || args[2].Value.kind != "number" || conditionalValueIsNonZero(args[2].Value) {
			return
		}
		lo, hi, c, valid := bounds(args[1], true)
		if !valid {
			return
		}
		p.kind, p.from, p.to, p.cols = "match", lo, hi, []int{c}
		q.terms = []*conditionalExpr{args[0]}
		return q, true
	case "COUNTIF", "COUNTIFS", "SUMIF", "SUMIFS":
	default:
		return
	}
	p.kind = "group"
	start, end := 0, len(args)
	if n.Op == "SUMIFS" {
		start = 1
	}
	if n.Op == "SUMIF" {
		end = 2
	}
	if (end-start)/2 > formats.ConditionalStatsColumns {
		return
	}
	for i := start; i < end; i += 2 {
		lo, hi, c, valid := bounds(args[i], true)
		if !valid {
			return
		}
		if i == start {
			p.from, p.to = lo, hi
		} else if lo != p.from || hi != p.to {
			return
		}
		p.cols = append(p.cols, c)
		term := args[i+1]
		q.terms = append(q.terms, term)
		literal := ""
		if term.Kind == cfExprLiteral {
			// Literal comparisons (including inequalities and wildcards) can be
			// applied once while building the index, independent of the target row.
			literal = term.Value.kind + ":" + term.Value.text
			if term.Value.kind == "number" {
				literal = "n:" + formatConditionalDecimalText(term.Value)
			}
			if term.Value.kind == "boolean" {
				literal = "b:" + strconv.FormatBool(term.Value.boolean)
			}
		}
		p.constants = append(p.constants, term.Value)
		p.literals = append(p.literals, literal)
	}
	if n.Op == "SUMIF" {
		p.sumCol = p.cols[0]
		if len(args) == 3 {
			r, c, _, _, valid := e.resolveRangeBounds(args[2], g)
			if !valid || r != p.from || args[2].Ref.ForeignSheet != "" || args[2].Ref.Row != 0 && !args[2].Ref.AbsoluteRow {
				return
			}
			p.sumCol = c
		}
	}
	if n.Op == "SUMIFS" {
		lo, hi, c, valid := bounds(args[0], true)
		if !valid || lo != p.from || hi != p.to {
			return
		}
		p.sumCol = c
	}
	return q, true
}

func (index *SourceConditionalFormats) statisticPlans() []conditionalStatPlan {
	var plans []conditionalStatPlan
	seen := map[string]bool{}
	for _, rule := range index.rules {
		if rule.predicateNotice != "" {
			continue
		}
		g := &index.groups[rule.group]
		e := ConditionalEvaluator{row: g.anchorRow, col: g.anchorCol}
		var walk func(*conditionalExpr)
		walk = func(n *conditionalExpr) {
			if len(plans) >= formats.ConditionalStatsPlans {
				return
			}
			if n.Kind == cfExprCall {
				if q, ok := e.statQuery(n, g); ok {
					key := q.plan.key()
					if !seen[key] {
						seen[key] = true
						plans = append(plans, q.plan)
					}
				}
			}
			for _, arg := range n.Args {
				walk(arg)
			}
		}
		for _, n := range rule.exprs {
			walk(n)
		}
	}
	return plans
}

func (r *conditionalRead) closeStatistics() {
	if r != nil && r.statistics != nil {
		r.statistics.Close()
		r.statistics = nil
	}
}

type boundStatKey struct {
	node *conditionalExpr
	col  int
}
type boundStatQuery struct {
	query conditionalStatQuery
	id    int
}

func (r *conditionalRead) indexedStatistic(n *conditionalExpr, g *conditionalGroup) (conditionalValue, string, error, bool) {
	e := r.evaluator
	key := boundStatKey{n, e.col}
	bound, ok := r.statBindings[key]
	if !ok {
		q, supported := e.statQuery(n, g)
		id := -1
		if supported {
			for i, p := range r.index.statistics {
				if p.kind == q.plan.kind && p.from == q.plan.from && p.to == q.plan.to && p.sumCol == q.plan.sumCol && slices.Equal(p.cols, q.plan.cols) && slices.Equal(p.literals, q.plan.literals) {
					id = i
					break
				}
			}

		}
		bound = boundStatQuery{q, id}
		if supported && len(r.statBindings) < formats.ConditionalRangeCacheEntries {
			if r.statBindings == nil {
				r.statBindings = make(map[boundStatKey]boundStatQuery)
			}
			r.statBindings[key] = bound
		}
	}
	q, id := bound.query, bound.id
	if id < 0 {
		return conditionalValue{}, "", nil, false
	}
	if q.plan.kind == "prefix" {
		lo, _, hi, _, valid := e.resolveRangeBounds(n.Args[0], g)
		if !valid {
			return conditionalErrorVal("#REF!"), "", nil, true
		}
		q.lo, q.hi = lo, hi
	}
	parts := make([]string, 0, len(q.terms))
	for i, t := range q.terms {
		if q.plan.kind == "group" && q.plan.literals[i] != "" {
			continue
		}
		v, notice, err := e.evalExpr(t, g)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err, true
		}
		if q.plan.kind == "match" && v.kind == "error" {
			return v, "", nil, true
		}
		key, valid := statLookupKey(v, q.plan.kind == "match")
		if !valid {
			return conditionalValue{}, "", nil, false
		}
		parts = append(parts, key)
	}
	if r.statistics == nil && r.statNotice == "" {
		r.statNotice = r.index.recentStatisticsIssue()
	}
	if r.statistics == nil && r.statNotice == "" {
		if err := r.openStatistics(); err != nil {
			if r.ctx.Err() != nil {
				return conditionalValue{}, "", r.ctx.Err(), true
			}
			r.statNotice = r.index.rememberStatisticsIssue(err)
		}
	}
	if r.statNotice != "" {
		return conditionalValue{}, r.statNotice, nil, false
	}
	var result conditional.Result
	var err error
	if q.plan.kind == "prefix" {
		result, err = r.statistics.Sum(id, q.lo, q.hi)
	} else {
		result, err = r.statistics.Lookup(id, conditional.Tuple(parts), q.plan.kind == "match")
	}
	if err != nil {
		return conditionalValue{}, "条件统计查询失败：" + conditionalNotice(err.Error()), nil, false
	}
	if result.Unknown {
		return conditionalValue{}, result.Fault, nil, true
	}
	if result.Fault != "" {
		return conditionalErrorVal(result.Fault), "", nil, true
	}
	switch n.Op {
	case "COUNTIF", "COUNTIFS":
		return conditionalIntVal(int64(result.Count)), "", nil, true
	case "MATCH":
		if result.First == 0 {
			return conditionalErrorVal("#N/A"), "", nil, true
		}
		return conditionalIntVal(int64(result.First - q.plan.from + 1)), "", nil, true
	}
	return conditionalSumResult(result.Number), "", nil, true
}

func statTypedKey(v conditionalValue) string {
	switch v.kind {
	case "number":
		d := v.number
		return "n" + strconv.FormatBool(d.negative) + d.digits + "e" + strconv.FormatInt(d.exponent, 10)
	case "text":
		return "t" + v.folded
	case "boolean":
		if v.boolean {
			return "b1"
		}
		return "b0"
	case "error":
		return "e" + v.text
	default:
		return "z"
	}
}

func statGroupKey(v conditionalValue) string {
	if v.kind == "text" {
		if v.text == "" {
			return "z"
		}
		if d, ok := parseConditionalDecimal(strings.TrimSpace(v.text)); ok {
			return statTypedKey(conditionalNumberVal(d))
		}
	}
	return statTypedKey(v)
}

func statLookupKey(v conditionalValue, match bool) (string, bool) {
	// A rounded calculated criterion may compare equal to several different
	// exact source keys. Only use the direct lookup if equality is exact.
	if v.calculatedFloat || v.rat != nil {
		return "", false
	}
	if match {
		if v.kind == "blank" {
			v = conditionalIntVal(0)
		}
		if v.kind == "text" && strings.ContainsAny(v.text, "*?~") {
			return "", false
		}
		return statTypedKey(v), true
	}
	if v.kind == "blank" {
		v = conditionalIntVal(0)
	}
	if v.kind == "text" {
		text := v.text
		if strings.HasPrefix(text, "<") || strings.HasPrefix(text, ">") {
			return "", false
		}
		text = strings.TrimPrefix(text, "=")
		if strings.ContainsAny(text, "*?~") || strings.HasPrefix(text, "#") {
			return "", false
		}
		if strings.EqualFold(text, "TRUE") || strings.EqualFold(text, "FALSE") {
			return statTypedKey(conditionalBoolVal(strings.EqualFold(text, "TRUE"))), true
		}
		v = conditionalText(text)
	}
	return statGroupKey(v), true
}

func (index *SourceConditionalFormats) recentStatisticsIssue() string {
	index.statsMu.Lock()
	defer index.statsMu.Unlock()
	if time.Now().Before(index.statsRetry) {
		return index.statsIssue
	}
	return ""
}
func (index *SourceConditionalFormats) rememberStatisticsIssue(err error) string {
	index.statsMu.Lock()
	defer index.statsMu.Unlock()
	index.statsIssue = "条件统计索引无法使用：" + conditionalNotice(err.Error())
	index.statsRetry = time.Now().Add(time.Minute)
	return index.statsIssue
}
func (r *conditionalRead) statisticsFile() (string, string) {
	h := sha256.New()
	fmt.Fprintf(h, "conditional-stats-v1\x00%s\x00%s\x00", r.w.Fingerprint, r.idx.SheetPath)
	for _, p := range r.index.statistics {
		fmt.Fprintln(h, p.key())
	}
	sig := hex.EncodeToString(h.Sum(nil))
	return filepath.Join(r.w.CacheDir, "conditional-v1-"+sig+".db"), sig
}
func (r *conditionalRead) openStatistics() error {
	file, sig := r.statisticsFile()
	idx, _, err := conditional.Open(r.ctx, file, sig, len(r.index.statistics), r.buildStatistics)
	r.statistics = idx
	return err
}

// Large visible sheets prepare statistics in the existing cancellable index
// job. Raw search, indexing for other consumers, and plain exports stay lazy.
func (w *Workbook) statisticsReader(ctx context.Context, meta SheetMeta, idx *SheetIndex) *conditionalRead {
	if idx == nil || idx.RowCount() <= formats.ConditionalDependencyReads {
		return nil
	}
	capability, ok := formats.Lookup(w.Format)
	if !ok || !capability.ConditionalMetadata {
		return nil
	}
	source, err := w.SourceConditionalFormats(ctx, meta, idx)
	if err != nil || source.notice != "" || len(source.statistics) == 0 || source.recentStatisticsIssue() != "" {
		return nil
	}
	return &conditionalRead{w: w, ctx: ctx, index: source, idx: idx}
}

func (w *Workbook) ConditionalStatisticsPending(ctx context.Context, meta SheetMeta, idx *SheetIndex) bool {
	r := w.statisticsReader(ctx, meta, idx)
	if r == nil {
		return false
	}
	file, _ := r.statisticsFile()
	_, err := os.Stat(file)
	return errors.Is(err, os.ErrNotExist)
}
func (w *Workbook) PrepareConditionalStatistics(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int, int)) error {
	r := w.statisticsReader(ctx, meta, idx)
	if r == nil {
		return ctx.Err()
	}
	r.statProgress = progress
	defer r.closeStatistics()
	if err := r.openStatistics(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.index.rememberStatisticsIssue(err)
	}
	return nil
}

func (r *conditionalRead) buildStatistics(w *conditional.Writer) error {
	plans := r.index.statistics
	columns := []int{}
	for _, p := range plans {
		columns = append(columns, p.cols...)
		if p.sumCol != 0 {
			columns = append(columns, p.sumCol)
		}
	}
	slices.Sort(columns)
	columns = slices.Compact(columns)
	if len(columns) == 0 {
		return errors.New("条件统计缺少引用列")
	}
	values := make(map[int]conditionalValue, len(columns))
	notices := make(map[int]string, len(columns))
	eval := r.index.NewEvaluator(r.ctx)
	// Static criteria share the builder's per-row budget, not the viewport's.
	filters := make([][]cfCriterion, len(plans))
	for i, p := range plans {
		if len(p.cols) == 0 {
			continue
		}
		filters[i] = make([]cfCriterion, len(p.cols))
		for j, literal := range p.literals {
			if literal != "" {
				filters[i][j] = eval.compileCFCriterion(p.constants[j])
			}
		}
	}
	accumulate := func(first, last int, gap bool) error {
		for id, p := range plans {
			a, b := max(first, p.from), min(last, p.to)
			if a > b {
				continue
			}
			value := func(col int) conditionalValue {
				if v, ok := values[col]; ok && !gap {
					return v
				}
				return conditionalBlankVal()
			}
			notice := func(col int) string {
				if gap {
					return ""
				}
				return notices[col]
			}
			if p.kind == "match" {
				v := value(p.cols[0])
				if why := notice(p.cols[0]); why != "" {
					if err := w.Issue(id, a, conditional.Value{Unknown: true, Fault: why}); err != nil {
						return err
					}
				} else if v.kind != "blank" {
					if err := w.Group(id, conditional.Tuple([]string{statTypedKey(v)}), a, b-a+1, conditional.Value{}); err != nil {
						return err
					}
				}
				continue
			}
			parts := make([]string, 0, len(p.cols))
			matched := true
			for j, col := range p.cols {
				if why := notice(col); why != "" {
					if err := w.Issue(id, a, conditional.Value{Unknown: true, Fault: why}); err != nil {
						return err
					}
					matched = false
					break
				}
				v := value(col)
				if filters[id][j] != nil {
					ok, msg := filters[id][j](v)
					if msg != "" {
						if err := w.Issue(id, a, conditional.Value{Unknown: true, Fault: msg}); err != nil {
							return err
						}
					}
					if !ok || msg != "" {
						matched = false
						break
					}
				} else {
					parts = append(parts, statGroupKey(v))
				}
			}
			if !matched {
				continue
			}
			var add conditional.Value
			if p.sumCol != 0 {
				v := value(p.sumCol)
				if why := notice(p.sumCol); why != "" {
					add = conditional.Value{Unknown: true, Fault: why}
				} else if v.kind == "number" {
					var ok bool
					add.Number, ok = valueToRat(v)
					if !ok {
						add.Fault = "#NUM!"
					}
				} else if v.kind == "error" {
					add.Fault = v.text
				}
			}
			var err error
			if p.kind == "prefix" {
				err = w.Prefix(id, a, add)
			} else {
				err = w.Group(id, conditional.Tuple(parts), a, b-a+1, add)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	lower, upper := formats.ExcelRows, 1
	for _, p := range plans {
		lower = min(lower, p.from)
		upper = max(upper, p.to)
	}
	startPos, endPos := r.idx.FindRowOffset(lower), r.idx.FindRowOffset(upper+1)
	visited := 0
	if r.statProgress != nil {
		r.statProgress(0, endPos-startPos)
	}
	prev := lower - 1
	positions := func(yield func(int) bool) {
		for pos := startPos; pos < endPos; pos++ {
			if !yield(pos) {
				return
			}
		}
	}
	err := r.w.VisitRowsWithOptionsContext(r.ctx, r.idx, positions, columns[0], columns[len(columns)-1], RowReadOptions{Columns: columns}, func(row RowData) error {
		if err := w.Next(); err != nil {
			return err
		}
		visited++
		if r.statProgress != nil && (visited%2048 == 0 || visited == endPos-startPos) {
			r.statProgress(visited, endPos-startPos)
		}
		eval.work, eval.textBytes = formats.ConditionalEvalWork, formats.ConditionalEvalTextBytes
		if row.Number > prev+1 {
			if err := accumulate(prev+1, row.Number-1, true); err != nil {
				return err
			}
		}
		clear(values)
		clear(notices)
		for _, c := range row.Cells {
			values[c.Col], notices[c.Col] = eval.value(c)
		}
		prev = row.Number
		return accumulate(prev, prev, false)
	})
	if err != nil {
		return err
	}
	if prev < upper {
		return accumulate(prev+1, upper, true)
	}
	return nil
}
