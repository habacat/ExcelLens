package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"excellens/internal/conditional"
	"excellens/internal/formats"
)

// ConditionalStatsState describes the readiness of a worksheet's disk conditional statistics.
type ConditionalStatsState string

const (
	ConditionalStatsNotNeeded ConditionalStatsState = "not_needed"
	ConditionalStatsReady     ConditionalStatsState = "ready"
	ConditionalStatsMissing   ConditionalStatsState = "missing"
	ConditionalStatsDamaged   ConditionalStatsState = "damaged"
)

// ConditionalStatsInfo reports whether disk statistics are required and verified on disk.
type ConditionalStatsInfo struct {
	State    ConditionalStatsState
	Plans    int
	FilePath string
}

// ConditionalStatsUsage summarizes disk usage and active protection in ConditionalStatsRoot.
type ConditionalStatsUsage struct {
	Files          int   `json:"files"`
	Bytes          int64 `json:"bytes"`
	ProtectedFiles int   `json:"protectedFiles"`
	ProtectedBytes int64 `json:"protectedBytes"`
}

// ConditionalStatsEviction summarizes LRU eviction performed for the global budget.
type ConditionalStatsEviction struct {
	EvictedFiles   int
	FreedBytes     int64
	RemainingBytes int64
	RemainingFiles int
}

// ConditionalStatsCleanup summarizes manual cleanup of conditional statistics caches.
type ConditionalStatsCleanup struct {
	RemovedFiles int
	FreedBytes   int64
	KeptFiles    int
	KeptBytes    int64
}

func InspectConditionalStatsCache(isProtected func(name string) bool) (ConditionalStatsUsage, error) {
	stats, err := conditional.InspectDir(ConditionalStatsRoot(), isProtected)
	return ConditionalStatsUsage{
		Files:          stats.Files,
		Bytes:          stats.Bytes,
		ProtectedFiles: stats.ProtectedFiles,
		ProtectedBytes: stats.ProtectedBytes,
	}, err
}

func EnforceConditionalStatsBudget(budgetBytes int64, isProtected func(name string) bool) (ConditionalStatsEviction, error) {
	report, err := conditional.EnforceBudget(conditional.BudgetPolicy{
		Dir:         ConditionalStatsRoot(),
		BudgetBytes: budgetBytes,
		Protected:   isProtected,
	}, 0, "")
	return ConditionalStatsEviction{
		EvictedFiles:   report.EvictedFiles,
		FreedBytes:     report.FreedBytes,
		RemainingBytes: report.RemainingBytes,
		RemainingFiles: report.RemainingFiles,
	}, err
}

func ClearUnusedConditionalStats(isProtected func(name string) bool) (ConditionalStatsCleanup, error) {
	report, err := conditional.ClearUnused(ConditionalStatsRoot(), isProtected)
	return ConditionalStatsCleanup{
		RemovedFiles: report.RemovedFiles,
		FreedBytes:   report.FreedBytes,
		KeptFiles:    report.KeptFiles,
		KeptBytes:    report.KeptBytes,
	}, err
}

func ClearAllConditionalStats() (ConditionalStatsCleanup, error) {
	return ClearUnusedConditionalStats(nil)
}

func ClearWorkbookConditionalStats(fingerprint string) error {
	return conditional.ClearPrefix(ConditionalStatsRoot(), ConditionalStatsFilePrefix(fingerprint))
}

func (index *SourceConditionalFormats) statisticPlans() []conditional.StatPlan {
	inputs := make([]conditional.RulePlanInput, 0, len(index.rules))
	for _, rule := range index.rules {
		if rule.predicateNotice != "" {
			continue
		}
		g := &index.groups[rule.group]
		inputs = append(inputs, conditional.RulePlanInput{
			AnchorRow: g.anchorRow,
			AnchorCol: g.anchorCol,
			Ranges:    g.areas,
			Exprs:     rule.exprs,
		})
	}
	return conditional.CollectDiskPlans(inputs)
}

func (r *conditionalRead) closeStatistics() {
	if r != nil && r.statistics != nil {
		_ = r.statistics.Close()
		r.statistics = nil
	}
}

type boundStatKey struct {
	node *conditionalExpr
	col  int
}

type boundStatQuery struct {
	query conditional.StatQuery
	id    int
}

func (r *conditionalRead) indexedStatistic(n *conditionalExpr, anchorRow, anchorCol int, ranges []conditional.Area) (conditionalValue, string, bool, error, bool) {
	eng := r.evaluator.engine
	key := boundStatKey{n, eng.Col}
	bound, ok := r.statBindings[key]
	if !ok {
		q, supported := conditional.AnalyzeStatQuery(n, anchorRow, anchorCol, ranges, eng.Row, eng.Col)
		id := -1
		if supported && q.Plan.Strategy.NeedsDiskIndex() {
			for i, p := range r.index.statistics {
				if p.Equal(q.Plan) {
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
		return conditionalValue{}, "", false, nil, false
	}
	if q.Plan.Kind == "prefix" {
		lo, _, hi, _, valid := eng.ResolveRangeBounds(n.Args[0], anchorRow, anchorCol)
		if !valid {
			return conditional.ErrorValue("#REF!"), "", false, nil, true
		}
		q.Lo, q.Hi = lo, hi
	}
	parts := make([]string, 0, len(q.Terms))
	for i, t := range q.Terms {
		if q.Plan.Kind == "group" && q.Plan.Literals[i] != "" {
			continue
		}
		v, notice, err := eng.Eval(t, anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, eng.Pending, err, true
		}
		if q.Plan.Kind == "match" && v.Kind == "error" {
			return v, "", false, nil, true
		}
		lookupKey, valid := conditional.LookupKey(v, q.Plan.Kind == "match")
		if !valid {
			return conditionalValue{}, "", false, nil, false
		}
		parts = append(parts, lookupKey)
	}

	if r.readOnlyStats {
		if r.statPending {
			return conditionalValue{}, r.statNotice, true, nil, true
		}
		if r.statistics == nil && r.statNotice == "" {
			err := r.openExistingStatistics()
			if err != nil {
				if errors.Is(err, conditional.ErrMissing) || errors.Is(err, conditional.ErrDamaged) {
					r.statNeedsBuild = true
					r.statDamaged = errors.Is(err, conditional.ErrDamaged)
					switch r.statsStatus {
					case "cancelled":
						r.statNotice = "条件统计索引已取消，尚未应用该规则样式"
						return conditionalValue{}, r.statNotice, false, nil, true
					case "error":
						if recent := r.index.recentStatisticsIssue(); recent != "" {
							r.statNotice = recent
						} else {
							r.statNotice = "条件统计索引建立失败，尚未应用该规则样式"
						}
						return conditionalValue{}, r.statNotice, false, nil, true
					default:
						r.statPending = true
						if r.statDamaged {
							r.statNotice = "条件统计索引重建中，尚未应用该规则样式"
						} else {
							r.statNotice = "条件统计索引计算中，尚未应用该规则样式"
						}
						return conditionalValue{}, r.statNotice, true, nil, true
					}
				}
				r.statNotice = r.index.rememberStatisticsIssue(err)
			}
		}
		if r.statNotice != "" {
			if r.statsStatus == "cancelled" || r.statsStatus == "error" {
				return conditionalValue{}, r.statNotice, false, nil, true
			}
			return conditionalValue{}, r.statNotice, false, nil, false
		}
	} else {
		if r.statistics == nil && r.statNotice == "" {
			r.statNotice = r.index.recentStatisticsIssue()
		}
		if r.statistics == nil && r.statNotice == "" {
			if err := r.openStatistics(nil); err != nil {
				if r.ctx.Err() != nil {
					return conditionalValue{}, "", false, r.ctx.Err(), true
				}
				r.statNotice = r.index.rememberStatisticsIssue(err)
			}
		}
		if r.statNotice != "" {
			return conditionalValue{}, r.statNotice, false, nil, false
		}
	}

	var result conditional.Result
	var err error
	if q.Plan.Kind == "prefix" {
		result, err = r.statistics.Sum(id, q.Lo, q.Hi)
	} else {
		result, err = r.statistics.Lookup(id, conditional.Tuple(parts), q.Plan.Kind == "match")
	}
	if err != nil {
		return conditionalValue{}, "条件统计查询失败：" + conditionalNotice(err.Error()), false, nil, false
	}
	if result.Unknown {
		return conditionalValue{}, result.Fault, false, nil, true
	}
	if result.Fault != "" {
		return conditional.ErrorValue(result.Fault), "", false, nil, true
	}
	switch n.Op {
	case "COUNTIF", "COUNTIFS":
		return conditional.IntValue(int64(result.Count)), "", false, nil, true
	case "MATCH":
		if result.First == 0 {
			return conditional.ErrorValue("#N/A"), "", false, nil, true
		}
		return conditional.IntValue(int64(result.First - q.Plan.From + 1)), "", false, nil, true
	}
	return conditional.SumResult(result.Number), "", false, nil, true
}

func (index *SourceConditionalFormats) recentStatisticsIssue() string {
	index.statsMu.Lock()
	defer index.statsMu.Unlock()
	if time.Now().Before(index.statsRetry) {
		return index.statsIssue
	}
	return ""
}

func (index *SourceConditionalFormats) clearStatisticsIssue() {
	index.statsMu.Lock()
	index.statsIssue = ""
	index.statsRetry = time.Time{}
	index.statsMu.Unlock()
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
		fmt.Fprintln(h, p.Key())
	}
	sig := hex.EncodeToString(h.Sum(nil))
	dir := ConditionalStatsRoot()
	return filepath.Join(dir, ConditionalStatsFilePrefix(r.w.Fingerprint)+sig+".db"), sig
}

func (r *conditionalRead) openExistingStatistics() error {
	file, sig := r.statisticsFile()
	idx, err := conditional.OpenExisting(file, sig, len(r.index.statistics))
	if err != nil {
		return err
	}
	r.statistics = idx
	return nil
}

func (r *conditionalRead) openStatistics(policy *conditional.BudgetPolicy) error {
	file, sig := r.statisticsFile()
	if policy == nil {
		idx, err := conditional.OpenExisting(file, sig, len(r.index.statistics))
		if err == nil {
			r.statistics = idx
			return nil
		}
		if !errors.Is(err, conditional.ErrMissing) && !errors.Is(err, conditional.ErrDamaged) {
			return err
		}
		r.w.conditionalMu.Lock()
		snapshot := r.w.conditionalPolicy
		r.w.conditionalMu.Unlock()
		policy = &conditional.BudgetPolicy{Dir: filepath.Dir(file), BudgetBytes: formats.ConditionalStatsDefaultBudgetBytes}
		if snapshot != nil {
			policy.BudgetBytes, policy.Protected = snapshot()
		}
	}
	idx, _, err := conditional.Build(r.ctx, file, sig, len(r.index.statistics), r.buildStatistics, policy)
	r.statistics = idx
	return err
}

// SetConditionalStatsPolicy supplies the current application budget and protected
// workbooks when a synchronous consumer (such as a formatted export) builds an index.
func (w *Workbook) SetConditionalStatsPolicy(snapshot func() (int64, func(string) bool)) {
	w.conditionalMu.Lock()
	defer w.conditionalMu.Unlock()
	w.conditionalPolicy = snapshot
}

func (w *Workbook) statisticsReader(ctx context.Context, meta SheetMeta, idx *SheetIndex, ignoreRecentIssue bool) *conditionalRead {
	if idx == nil || idx.RowCount() < formats.ConditionalStatsMinRows {
		return nil
	}
	capability, ok := formats.Lookup(w.Format)
	if !ok || !capability.ConditionalMetadata {
		return nil
	}
	source, err := w.SourceConditionalFormats(ctx, meta, idx)
	if err != nil || source.notice != "" || len(source.statistics) == 0 {
		return nil
	}
	if !ignoreRecentIssue && source.recentStatisticsIssue() != "" {
		return nil
	}
	return &conditionalRead{w: w, ctx: ctx, index: source, idx: idx}
}

// ConditionalStatisticsState inspects the disk statistics file for meta without
// rebuilding it, accurately distinguishing missing and damaged/zero-byte caches.
func (w *Workbook) ConditionalStatisticsState(ctx context.Context, meta SheetMeta, idx *SheetIndex) (ConditionalStatsInfo, error) {
	r := w.statisticsReader(ctx, meta, idx, true)
	if r == nil {
		return ConditionalStatsInfo{State: ConditionalStatsNotNeeded}, nil
	}
	file, sig := r.statisticsFile()
	state, err := conditional.Check(file, sig, len(r.index.statistics))
	info := ConditionalStatsInfo{Plans: len(r.index.statistics), FilePath: file}
	if err != nil {
		info.State = ConditionalStatsMissing
		return info, err
	}
	switch state {
	case conditional.StateReady:
		info.State = ConditionalStatsReady
	case conditional.StateDamaged:
		info.State = ConditionalStatsDamaged
	default:
		info.State = ConditionalStatsMissing
	}
	return info, nil
}

// ConditionalStatisticsPending reports whether a worksheet requires background
// conditional statistics preparation or repair before disk-indexed rules can apply.
func (w *Workbook) ConditionalStatisticsPending(ctx context.Context, meta SheetMeta, idx *SheetIndex) bool {
	info, err := w.ConditionalStatisticsState(ctx, meta, idx)
	if err != nil {
		return false
	}
	return info.State == ConditionalStatsMissing || info.State == ConditionalStatsDamaged
}

// PrepareConditionalStatistics builds or repairs disk conditional statistics using
// the default cross-workbook capacity budget.
func (w *Workbook) PrepareConditionalStatistics(ctx context.Context, meta SheetMeta, idx *SheetIndex, progress func(int, int)) error {
	return w.PrepareConditionalStatisticsWithPolicy(ctx, meta, idx, formats.ConditionalStatsDefaultBudgetBytes, nil, progress)
}

// PrepareConditionalStatisticsWithPolicy builds or repairs disk conditional statistics
// in a cancellable background task while enforcing the global cumulative cache budget.
func (w *Workbook) PrepareConditionalStatisticsWithPolicy(ctx context.Context, meta SheetMeta, idx *SheetIndex, budgetBytes int64, isProtected func(name string) bool, progress func(int, int)) error {
	r := w.statisticsReader(ctx, meta, idx, true)
	if r == nil {
		return ctx.Err()
	}
	r.statProgress = progress
	defer r.closeStatistics()
	policy := &conditional.BudgetPolicy{
		Dir:         ConditionalStatsRoot(),
		BudgetBytes: budgetBytes,
		Protected:   isProtected,
	}
	if err := r.openStatistics(policy); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.index.rememberStatisticsIssue(err)
		return err
	}
	r.index.clearStatisticsIssue()
	return nil
}

func (r *conditionalRead) buildStatistics(w *conditional.Writer) error {
	builder := conditional.NewRowAggregator(r.ctx, w, r.index.statistics, r.index.date1904)
	columns := builder.Columns()
	if len(columns) == 0 {
		return errors.New("条件统计缺少引用列")
	}
	lower, upper := builder.Bounds()
	start, end := r.idx.FindRowOffset(lower), r.idx.FindRowOffset(upper+1)
	if r.statProgress != nil {
		r.statProgress(0, end-start)
	}
	positions := func(yield func(int) bool) {
		for pos := start; pos < end; pos++ {
			if !yield(pos) {
				return
			}
		}
	}
	visited := 0
	scratch := make([]CellData, 0, len(columns))
	err := r.w.VisitRowsWithOptionsContext(r.ctx, r.idx, positions, columns[0], columns[len(columns)-1], RowReadOptions{Columns: columns, cellScratch: &scratch}, func(row RowData) error {
		if err := builder.Next(row.Number); err != nil {
			return err
		}
		for i := range row.Cells {
			c := &row.Cells[i]
			builder.Set(c.Col, conditional.CellOperand{Raw: c.Raw, Type: c.Type, CacheMissing: c.CacheMissing})
		}
		clear(row.Cells)
		visited++
		if r.statProgress != nil && (visited%2048 == 0 || visited == end-start) {
			r.statProgress(visited, end-start)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return builder.Finish()
}
