package workbook

import (
	"context"
	"errors"
	"math"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"excellens/internal/formats"
	"excellens/internal/sourceview"
)

type FilterRule = sourceview.FilterRule
type SortKey = sourceview.SortKey

type FilterOptions struct {
	// HeaderRow is a real Excel row number, not an index into the compact
	// row-offset list. Header rows are retained and never tested as data.
	HeaderRow  int
	SortColumn int
	Descending bool
	SortKeys   []SortKey
	// Phase reports sorting/merging after the source scan, without exposing
	// partial row orders. Optional; not part of the saved view description.
	Phase func(stage string, done, total int) `json:"-"`
}

type DistinctValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// DistinctColumnValues reads one source column per indexed row without loading
// the workbook into RAM. A bounded result is intentional for high-cardinality
// columns; callers MUST display Truncated instead of implying completeness.
func (w *Workbook) DistinctColumnValues(idx *SheetIndex, column, headerRow, limit int, cancel <-chan struct{}) ([]DistinctValue, bool, error) {
	if column < 1 || column > formats.ExcelColumns || headerRow < 0 || headerRow > MaxRowsForIndex(idx) || limit < 1 || limit > 10000 {
		return nil, false, errors.New("invalid distinct values request")
	}
	views, cached := w.cachedColumns(idx, []int{column})
	var f *os.File
	if !cached {
		var err error
		f, err = os.Open(idx.XMLPath)
		if err != nil {
			return nil, false, err
		}
		defer f.Close()
	}
	var builder *cachedColumn
	if !cached {
		builder = beginColumnCache(idx.RowCount())
	}
	items := make([]DistinctValue, 0, 64)
	indices := make(map[string]int, 64)
	var distinctBytes int64
	stream := indexedStream{f: f, idx: idx}
	for i, entry := range idx.RowOffsets() {
		if entry.Number <= headerRow {
			if builder != nil {
				row, err := stream.read(w, i, column, column, nil, true, true)
				if err != nil {
					return nil, false, err
				}
				for _, cell := range row.Cells {
					if cell.Col == column {
						builder = writeColumnCache(builder, i, cell, true)
						break
					}
				}
			}
			continue
		}
		if i&31 == 0 {
			select {
			case <-cancel:
				return nil, false, errors.New("distinct cancelled")
			default:
			}
		}
		value := ""
		if cached {
			value = views[0].valueAt(i)
		} else {
			row, err := stream.read(w, i, column, column, nil, true, true)
			if err != nil {
				return nil, false, err
			}
			if len(row.Cells) > 0 {
				for _, cell := range row.Cells {
					if cell.Col == column {
						value = filterValue(cell)
						if builder != nil {
							builder = writeColumnCache(builder, i, cell, true)
						}
						break
					}
				}
			}
		}
		if pos, found := indices[value]; found {
			items[pos].Count++
			continue
		}
		if len(items) == limit || distinctBytes+int64(len(value))+64 > 32<<20 {
			return items, true, nil
		}
		distinctBytes += int64(len(value)) + 64
		indices[value] = len(items)
		items = append(items, DistinctValue{Value: value, Count: 1})
	}
	if builder != nil {
		w.cacheColumn(idx, column, builder)
	}
	sort.SliceStable(items, func(i, j int) bool { return strings.ToLower(items[i].Value) < strings.ToLower(items[j].Value) })
	return items, false, nil
}

// FilterSheet evaluates every indexed row while decoding only the columns used
// by the active predicates. The returned values are positions in idx.Rows and
// remain sorted, allowing fast paging and row-address lookup.
func (w *Workbook) FilterSheet(idx *SheetIndex, rules []FilterRule, cancel <-chan struct{}, progress func(done, total int)) ([]int, error) {
	return w.FilterSheetWithOptions(idx, rules, FilterOptions{}, cancel, progress)
}

// compiledFilterRule avoids repeated string folding and O(2500) linear scans
// of the same permitted-values list for every physical worksheet row.
type compiledFilterRule struct {
	FilterRule
	slot         int
	needle       string
	allowed      map[string]struct{}
	allowedExact map[string]struct{} // frequent case-sensitive hit avoids folding allocation
	numericValue float64
	hasNumeric   bool
}

type sortValue struct {
	text     string
	folded   string
	number   float64
	isNumber bool
}

// A narrow row record indexes a tightly-packed, contiguous comparison-key
// buffer. One-key sorts no longer reserve four unused keys per row (which
// wasted ~200 MB per million matching rows). Sorting remains stable.
type sortItem struct {
	index  int
	offset int
}

func (w *Workbook) FilterSheetWithOptions(idx *SheetIndex, rules []FilterRule, opts FilterOptions, cancel <-chan struct{}, progress func(done, total int)) ([]int, error) {
	ctx := filterCancelContext{Context: context.Background(), cancel: cancel}
	rows, err := w.FilterRowOrder(ctx, idx, rules, opts, progress)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Compatibility for existing in-process callers requesting a materialized
	// slice. Production view/export routes retain RowOrder directly.
	positions := make([]int, 0, rows.Len())
	for position := range rows.Iter() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		positions = append(positions, position)
	}
	return positions, nil
}

type filterCancelContext struct {
	context.Context
	cancel <-chan struct{}
}

func (c filterCancelContext) Done() <-chan struct{} { return c.cancel }
func (c filterCancelContext) Err() error {
	select {
	case <-c.cancel:
		return context.Canceled
	default:
		return nil
	}
}

// ValidateFilterSpec checks saved/replayed rules without scanning source rows.
// Execution uses the same validation, so a saved view has no separate DSL.
func ValidateFilterSpec(idx *SheetIndex, rules []FilterRule, opts FilterOptions) error {
	_, err := validatedFilterKeys(idx, rules, opts)
	return err
}

func validatedFilterKeys(idx *SheetIndex, rules []FilterRule, opts FilterOptions) ([]SortKey, error) {
	if len(rules) == 0 && opts.SortColumn == 0 && len(opts.SortKeys) == 0 {
		return nil, errors.New("至少需要一个筛选条件")
	}
	if idx == nil || len(rules) > formats.FilterRuleCount {
		return nil, errors.New("筛选来源无效或超过32个条件")
	}
	if opts.HeaderRow < 0 || opts.HeaderRow > MaxRowsForIndex(idx) || opts.SortColumn < 0 || opts.SortColumn > formats.ExcelColumns {
		return nil, errors.New("筛选表头或排序列无效")
	}
	keys := opts.SortKeys
	if len(keys) == 0 && opts.SortColumn > 0 {
		keys = []SortKey{{Column: opts.SortColumn, Descending: opts.Descending}}
	}
	if len(keys) > formats.SortKeyCount {
		return nil, errors.New("最多允许5个排序条件")
	}
	seenSort := map[int]bool{}
	for _, key := range keys {
		if key.Column < 1 || key.Column > formats.ExcelColumns || seenSort[key.Column] {
			return nil, errors.New("排序列无效或重复")
		}
		if key.Mode != "" && key.Mode != "number" && key.Mode != "text" && key.Mode != "natural" {
			return nil, errors.New("排序方式无效")
		}
		seenSort[key.Column] = true
	}
	for _, rule := range rules {
		if rule.Column < 1 || rule.Column > formats.ExcelColumns {
			return nil, errors.New("筛选列超出范围")
		}
		if !validFilterOperator(rule.Operator) {
			return nil, errors.New("不支持的筛选条件")
		}
		if rule.Operator == "in" && (len(rule.Values) == 0 || len(rule.Values) > formats.FilterSetValues) {
			return nil, errors.New("筛选值清单为空或过长")
		}
		if rule.Operator != "in" && rule.Operator != "blank" && rule.Operator != "notBlank" && strings.TrimSpace(rule.Value) == "" {
			return nil, errors.New("筛选值不能为空")
		}
	}
	return keys, nil
}

func (w *Workbook) FilterRowOrder(ctx context.Context, idx *SheetIndex, rules []FilterRule, opts FilterOptions, progress func(done, total int)) (*RowOrder, error) {
	return w.filterRowOrder(ctx, idx, rules, opts, progress)
}
func makeSortValue(cell CellData, mode string) sortValue {
	value := filterValue(cell)
	// Compare saved numeric values, not rounded currency strings or date
	// labels. Explicit text mode sorts exactly what the viewer displays.
	if mode != "text" && mode != "natural" && (cell.Type == "" || cell.Type == "n") && cell.Raw != "" {
		value = cell.Raw
	}
	var n float64
	var numeric bool
	if mode != "text" && mode != "natural" && !(mode == "" && isTextCell(cell)) {
		n, numeric = parseFilterNumber(value)
	}
	if numeric && strings.HasSuffix(strings.TrimSpace(value), "%") {
		n /= 100
	}
	// Own only the key bytes: a small cached substring can otherwise retain
	// a whole source record/dictionary in every spilled chunk.
	key := sortValue{text: strings.Clone(value), number: n, isNumber: numeric}
	if !numeric {
		key.folded = strings.ToLower(key.text)
	}
	return key
}

func compareSortNumeric(a, b string, af, bf float64) int {
	// Percent scaling can introduce a second floating-point rounding. Resolve
	// close scaled values exactly as well as ordinary float ties.
	percent := strings.HasSuffix(strings.TrimSpace(a), "%") || strings.HasSuffix(strings.TrimSpace(b), "%")
	magnitude := math.Max(math.Abs(af), math.Abs(bf))
	closeScaled := percent && math.Abs(af-bf) <= 8*(math.Nextafter(magnitude, math.Inf(1))-magnitude)
	if af < bf && !closeScaled {
		return -1
	}
	if af > bf && !closeScaled {
		return 1
	}
	if a == b {
		return 0
	}
	ar, aok := exactNumericRat(a)
	br, bok := exactNumericRat(b)
	if aok && bok {
		if strings.HasSuffix(strings.TrimSpace(a), "%") {
			ar.Quo(ar, big.NewRat(100, 1))
		}
		if strings.HasSuffix(strings.TrimSpace(b), "%") {
			br.Quo(br, big.NewRat(100, 1))
		}
		return ar.Cmp(br)
	}
	return 0
}

func foldRune(r rune) rune {
	if r < 0x80 {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
	minimum := r
	for folded := unicode.SimpleFold(r); folded != r; folded = unicode.SimpleFold(folded) {
		if folded < minimum {
			minimum = folded
		}
	}
	return minimum
}

// filterFoldKey is canonically equivalent to strings.EqualFold, not merely
// ToLower. Unicode simple-fold groups include U+017F (long s) and the Kelvin
// sign; dropping that distinction would make optimized filters incorrect.
func filterFoldKey(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToUpper(s)
	}
	var out strings.Builder
	out.Grow(len(s))
	for _, r := range s {
		out.WriteRune(foldRune(r))
	}
	return out.String()
}

func isTextCell(cell CellData) bool {
	switch cell.Type {
	case "s", "str", "inlineStr":
		return true
	}
	return false
}

func matchCompiledFilter(cell CellData, exists bool, rule compiledFilterRule) bool {
	value := ""
	if exists {
		value = filterValue(cell)
	}
	switch rule.Operator {
	case "in":
		if _, ok := rule.allowedExact[value]; ok {
			return true
		}
		_, ok := rule.allowed[filterFoldKey(value)]
		return ok
	case "blank":
		return !exists || strings.TrimSpace(value) == ""
	case "notBlank":
		return exists && strings.TrimSpace(value) != ""
	}
	folded := strings.ToLower(value)
	switch rule.Operator {
	case "contains":
		return strings.Contains(folded, rule.needle)
	case "notContains":
		return !strings.Contains(folded, rule.needle)
	case "startsWith":
		return strings.HasPrefix(folded, rule.needle)
	case "endsWith":
		return strings.HasSuffix(folded, rule.needle)
	case "equals", "notEquals", "gt", "gte", "lt", "lte":
		left, leftOK := parseFilterNumber(value)
		// Text cells such as an account number with leading zeroes must not
		// silently become a float64 (and lose distinct digits).
		numeric := leftOK && rule.hasNumeric && !isTextCell(cell)
		if rule.Operator == "equals" || rule.Operator == "notEquals" {
			equal := folded == rule.needle
			if numeric {
				equal = compareFilterNumeric(value, rule.Value, left, rule.numericValue) == 0
			}
			if rule.Operator == "equals" {
				return equal
			}
			return !equal
		}
		if !numeric {
			return false
		}
		cmp := compareFilterNumeric(value, rule.Value, left, rule.numericValue)
		switch rule.Operator {
		case "gt":
			return cmp > 0
		case "gte":
			return cmp >= 0
		case "lt":
			return cmp < 0
		default:
			return cmp <= 0
		}
	}
	return false
}

// Numeric float64 comparisons are fast for ordinary Excel numbers. For
// identifiers or decimals beyond float64's exact integer range, a tie must
// be broken using exact base-10 arithmetic, not rounded binary floats.
// It only takes the slower branch when rounded float64 values are identical.
func compareFilterNumeric(a, b string, af, bf float64) int {
	if af < bf {
		return -1
	}
	if af > bf {
		return 1
	}
	if a == b {
		return 0
	}
	ar, aok := exactNumericRat(a)
	br, bok := exactNumericRat(b)
	if aok && bok {
		return ar.Cmp(br)
	}
	return 0 // same representable number, e.g. rounding in formatting
}

func exactNumericRat(value string) (*big.Rat, bool) {
	s := strings.TrimSpace(value)
	negative := false
	if len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		negative = true
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "￥", "")
	s = strings.ReplaceAll(s, "¥", "")
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, "元", "")
	s = strings.ReplaceAll(s, "人民币", "")
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, false
	}
	if negative {
		r.Neg(r)
	}
	return r, true
}

func validFilterOperator(operator string) bool {
	switch operator {
	case "contains", "notContains", "equals", "notEquals", "startsWith", "endsWith", "gt", "gte", "lt", "lte", "blank", "notBlank", "in":
		return true
	default:
		return false
	}
}

func matchFilter(cell CellData, exists bool, rule FilterRule) bool {
	compiled := compiledFilterRule{FilterRule: rule, needle: strings.ToLower(strings.TrimSpace(rule.Value))}
	if rule.Operator == "in" {
		compiled.allowed = make(map[string]struct{}, len(rule.Values))
		compiled.allowedExact = make(map[string]struct{}, len(rule.Values))
		for _, v := range rule.Values {
			compiled.allowedExact[v] = struct{}{}
			compiled.allowed[filterFoldKey(v)] = struct{}{}
		}
	}
	compiled.numericValue, compiled.hasNumeric = parseFilterNumber(strings.TrimSpace(rule.Value))
	return matchCompiledFilter(cell, exists, compiled)
}

func filterValue(cell CellData) string {
	if cell.Display != "" {
		return cell.Display
	}
	return cell.Raw
}

// parseFilterNumber accepts common Excel display forms (thousands separators,
// currency suffixes, percentages and accounting-style negative numbers).
// Percentages are compared in their displayed percentage units, so 12.5%
// compares equal to an entered filter value of 12.5% or 12.5.
func parseFilterNumber(value string) (float64, bool) {
	s := strings.TrimSpace(value)
	if s == "" {
		return 0, false
	}
	negative := false
	if len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		negative = true
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "￥", "")
	s = strings.ReplaceAll(s, "¥", "")
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, "元", "")
	s = strings.ReplaceAll(s, "人民币", "")
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	if negative {
		n = -n
	}
	return n, true
}
