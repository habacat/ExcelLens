package workbook

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"math/bits"
	"slices"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// Geometry uses integer nanopts. Raw source units/attributes are retained;
// column font conversion and sub-nanopt quantization are explicitly approximate.
type PrintAxisProperty struct {
	Attributes map[string]string `json:"attributes"`
	RawSize    string            `json:"rawSize,omitempty"`
	Unit       string            `json:"unit"`
	Points     float64           `json:"points"`
	Status     string            `json:"status"`
	Reason     string            `json:"reason,omitempty"`
	Hidden     bool              `json:"hidden"`
	HiddenSet  bool              `json:"hiddenSet"`
}

type PrintAxisSegment struct {
	First    int               `json:"first"`
	Last     int               `json:"last"`
	Property PrintAxisProperty `json:"property"`
}

type PrintGeometrySummary struct {
	Sheet          string            `json:"sheet"`
	Format         string            `json:"format"`
	Complete       bool              `json:"complete"`
	RowSegments    int               `json:"rowSegments"`
	ColumnSegments int               `json:"columnSegments"`
	DefaultRow     PrintAxisProperty `json:"defaultRow"`
	DefaultColumn  PrintAxisProperty `json:"defaultColumn"`
	NormalFont     string            `json:"normalFont,omitempty"`
	DigitWidth96   float64           `json:"digitWidth96"`
	Notices        []string          `json:"notices,omitempty"`
}

type PrintGeometryPage struct {
	Total int                `json:"total"`
	Items []PrintAxisSegment `json:"items"`
}

type PrintGeometryExtent struct {
	Units       int64   `json:"units,string"`
	Points      float64 `json:"points"`
	Approximate bool    `json:"approximate"`
	Defaulted   bool    `json:"defaulted"`
}

type printAxisSpec struct {
	raw, size, unit, status, reason string
	units                           int64
	hidden, hiddenSet               bool
	missingDefault                  bool
}

func (s printAxisSpec) property() PrintAxisProperty {
	attributes := make(map[string]string)
	_ = json.Unmarshal([]byte(s.raw), &attributes)
	return PrintAxisProperty{attributes, s.size, s.unit, float64(s.units) / float64(formats.PrintGeometryUnitsPerPoint), s.status, s.reason, s.hidden, s.hiddenSet}
}

type printAxisRule struct{ first, last, spec uint32 }
type printGeometryRun struct {
	end   uint32
	flags uint32
	units int64
}
type printGeometryPrefix struct {
	lo, hi                         uint64
	unknown, approximate, fallback uint32
}
type printGeometryAxis struct {
	maximum     int
	rules       []printAxisRule
	runs        []printGeometryRun
	prefix      []printGeometryPrefix
	defaultSpec uint32
}

type SourcePrintGeometryIndex struct {
	sheet, format, normalFont string
	digit                     float64
	complete                  bool
	specs                     []printAxisSpec
	rows, columns             printGeometryAxis
	notices                   []string
	bytes                     int64
}

func (index *SourcePrintGeometryIndex) BudgetBytes() int64 { return index.bytes }

func (index *SourcePrintGeometryIndex) Summary() PrintGeometrySummary {
	return PrintGeometrySummary{index.sheet, index.format, index.complete, len(index.rows.rules), len(index.columns.rules), index.specs[index.rows.defaultSpec].property(), index.specs[index.columns.defaultSpec].property(), index.normalFont, index.digit, slices.Clone(index.notices)}
}

func (index *SourcePrintGeometryIndex) axis(axis string) (*printGeometryAxis, error) {
	if axis == "row" {
		return &index.rows, nil
	}
	if axis == "col" {
		return &index.columns, nil
	}
	return nil, errors.New("打印几何行列轴无效")
}

func (index *SourcePrintGeometryIndex) Page(axis string, offset, limit int) (PrintGeometryPage, error) {
	a, err := index.axis(axis)
	if err != nil {
		return PrintGeometryPage{}, err
	}
	if offset < 0 || offset > len(a.rules) || limit < 1 || limit > formats.PrintGeometryPageRows {
		return PrintGeometryPage{}, errors.New("打印尺寸分页范围无效")
	}
	page := PrintGeometryPage{Total: len(a.rules), Items: []PrintAxisSegment{}}
	for _, rule := range a.rules[offset:min(len(a.rules), offset+limit)] {
		page.Items = append(page.Items, PrintAxisSegment{int(rule.first), int(rule.last), index.specs[rule.spec].property()})
	}
	return page, nil
}

func (a *printGeometryAxis) prefixAt(position int) printGeometryPrefix {
	if position == 0 {
		return printGeometryPrefix{}
	}
	at := sort.Search(len(a.runs), func(i int) bool { return int(a.runs[i].end) >= position })
	var result printGeometryPrefix
	start := 0
	if at > 0 {
		result, start = a.prefix[at-1], int(a.runs[at-1].end)
	}
	return addPrintGeometryLength(result, a.runs[at], position-start)
}

func addPrintGeometryLength(prefix printGeometryPrefix, run printGeometryRun, count int) printGeometryPrefix {
	hi, lo := bits.Mul64(uint64(run.units), uint64(count))
	var carry uint64
	prefix.lo, carry = bits.Add64(prefix.lo, lo, 0)
	prefix.hi, _ = bits.Add64(prefix.hi, hi, carry)
	if run.flags&(1|4) != 0 {
		prefix.unknown += uint32(count)
	}
	if run.flags&4 != 0 {
		prefix.fallback += uint32(count)
	}
	if run.flags&2 != 0 {
		prefix.approximate += uint32(count)
	}
	return prefix
}

// Inclusive source coordinates. A missing/invalid visible size fails only a
// range that actually intersects it. Hidden source rows/columns contribute zero.
// Wide prefixes avoid cancellation and overflow for small ranges after huge ones.
func (index *SourcePrintGeometryIndex) Extent(ctx context.Context, axis string, first, last int) (PrintGeometryExtent, error) {
	if err := ctx.Err(); err != nil {
		return PrintGeometryExtent{}, err
	}
	a, err := index.axis(axis)
	if err != nil {
		return PrintGeometryExtent{}, err
	}
	if first < 1 || last < first || last > a.maximum {
		return PrintGeometryExtent{}, errors.New("打印几何源范围越界")
	}
	before, after := a.prefixAt(first-1), a.prefixAt(last)
	if after.unknown != before.unknown {
		return PrintGeometryExtent{}, errors.New("打印范围含缺少或无法解释的行列尺寸，未补成默认长度")
	}
	lo, borrow := bits.Sub64(after.lo, before.lo, 0)
	hi, _ := bits.Sub64(after.hi, before.hi, borrow)
	if hi != 0 || lo > math.MaxInt64 {
		return PrintGeometryExtent{}, errors.New("打印范围物理长度超过计算容量")
	}
	return PrintGeometryExtent{Units: int64(lo), Points: float64(lo) / float64(formats.PrintGeometryUnitsPerPoint), Approximate: after.approximate != before.approximate}, nil
}

type printGeometryBuilder struct {
	index        SourcePrintGeometryIndex
	ids          map[printAxisSpec]uint32
	allocated    int64
	columnWork   int
	colIDs       []uint32
	rowSeen      int
	formatSeen   bool
	measureCache map[printGeometryMeasureKey]printGeometryMeasurement
}

type printGeometryMeasureKey struct{ raw, unit string }
type printGeometryMeasurement struct {
	units          int64
	status, reason string
}

func (b *printGeometryBuilder) measure(raw, unit string) (int64, string, string) {
	key := printGeometryMeasureKey{raw, unit}
	if value, found := b.measureCache[key]; found {
		return value.units, value.status, value.reason
	}
	units, status, reason := printGeometryMeasure(raw, unit, b.index.digit)
	if len(b.measureCache) < formats.PrintGeometryMeasureCache && len(raw) <= formats.PrintGeometryMeasureBytes {
		if b.measureCache == nil {
			b.measureCache = make(map[printGeometryMeasureKey]printGeometryMeasurement)
		}
		b.measureCache[key] = printGeometryMeasurement{units, status, reason}
	}
	return units, status, reason
}

func newPrintGeometryBuilder(sheet, format, font string, digit float64) *printGeometryBuilder {
	source, _ := formats.Lookup(format)
	b := &printGeometryBuilder{index: SourcePrintGeometryIndex{sheet: sheet, format: format, normalFont: font, digit: digit, complete: true, rows: printGeometryAxis{maximum: source.MaxRows}, columns: printGeometryAxis{maximum: source.MaxColumns}}, ids: make(map[printAxisSpec]uint32), colIDs: make([]uint32, source.MaxColumns)}
	b.index.specs = []printAxisSpec{{raw: "{}", unit: "inherit", status: "absent"}}
	return b
}

func (b *printGeometryBuilder) notice(reason string) {
	b.index.complete = false
	if !slices.Contains(b.index.notices, reason) && len(b.index.notices) < formats.PrintNoticeCount {
		b.index.notices = append(b.index.notices, reason)
	}
}

func (b *printGeometryBuilder) reserve(bytes int64) error {
	if bytes < 0 || bytes > formats.PrintGeometryBytes-b.allocated {
		return errors.New("源打印尺寸超过读取容量，未发布部分几何")
	}
	b.allocated += bytes
	return nil
}

func (b *printGeometryBuilder) intern(spec printAxisSpec) (uint32, error) {
	if id, found := b.ids[spec]; found {
		return id, nil
	}
	if err := b.reserve(int64(len(spec.raw) + len(spec.size) + len(spec.reason) + 192)); err != nil {
		return 0, err
	}
	id := uint32(len(b.index.specs))
	b.ids[spec] = id
	b.index.specs = append(b.index.specs, spec)
	if spec.status == "invalid" || spec.status == "unresolved" {
		b.notice(spec.reason)
	}
	return id, nil
}

func (b *printGeometryBuilder) addRow(row int, spec printAxisSpec) error {
	if row <= b.rowSeen || row > b.index.rows.maximum {
		return errors.New("源打印行尺寸坐标重复、逆序或越界")
	}
	b.rowSeen = row
	id, err := b.intern(spec)
	if err != nil {
		return err
	}
	rules := &b.index.rows.rules
	if n := len(*rules); n > 0 && int((*rules)[n-1].last)+1 == row && (*rules)[n-1].spec == id {
		(*rules)[n-1].last = uint32(row)
		return nil
	}
	if len(*rules) >= formats.PrintGeometryAxisRanges {
		return errors.New("源打印行尺寸段数超过容量")
	}
	if err := b.reserve(24); err != nil {
		return err
	}
	*rules = append(*rules, printAxisRule{uint32(row), uint32(row), id})
	return nil
}

func (b *printGeometryBuilder) addColumns(first, last int, spec printAxisSpec) error {
	if first < 1 || last < first || last > b.index.columns.maximum {
		return errors.New("源打印列尺寸坐标越界")
	}
	count := last - first + 1
	if count > formats.PrintGeometryColumnWork-b.columnWork {
		return errors.New("源打印列覆盖次数超过工作容量")
	}
	b.columnWork += count
	id, err := b.intern(spec)
	if err != nil {
		return err
	}
	for at := first - 1; at < last; at++ {
		b.colIDs[at] = id
	}
	return nil
}

func (b *printGeometryBuilder) finish(ctx context.Context) (*SourcePrintGeometryIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for first := 0; first < len(b.colIDs); {
		last := first + 1
		for last < len(b.colIDs) && b.colIDs[last] == b.colIDs[first] {
			last++
		}
		if b.colIDs[first] != 0 {
			if err := b.reserve(24); err != nil {
				return nil, err
			}
			b.index.columns.rules = append(b.index.columns.rules, printAxisRule{uint32(first + 1), uint32(last), b.colIDs[first]})
		}
		first = last
	}
	for _, axis := range []*printGeometryAxis{&b.index.rows, &b.index.columns} {
		if axis.defaultSpec == 0 {
			id, err := b.intern(printAxisSpec{raw: "{}", unit: "inherit", status: "unresolved", reason: "源未保存可靠的默认行列尺寸，未猜测字体或尺寸", missingDefault: true})
			if err != nil {
				return nil, err
			}
			axis.defaultSpec = id
		}
		if source := b.index.specs[axis.defaultSpec]; source.size == "" && source.status == "inherit" {
			source.missingDefault = true
			source.status, source.reason = "unresolved", "源未保存可靠的默认行列尺寸，未猜测字体或尺寸"
			id, err := b.intern(source)
			if err != nil {
				return nil, err
			}
			axis.defaultSpec = id
		}
		first := 1
		add := func(last int, spec printAxisSpec) {
			if first > last {
				return
			}
			run := effectivePrintGeometry(spec, b.index.specs[axis.defaultSpec])
			run.end = uint32(last)
			if n := len(axis.runs); n > 0 && axis.runs[n-1].units == run.units && axis.runs[n-1].flags == run.flags {
				axis.runs[n-1].end = run.end
			} else {
				axis.runs = append(axis.runs, run)
			}
			first = last + 1
		}
		for _, rule := range axis.rules {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			add(int(rule.first)-1, b.index.specs[axis.defaultSpec])
			add(int(rule.last), b.index.specs[rule.spec])
		}
		add(axis.maximum, b.index.specs[axis.defaultSpec])
		if err := b.reserve(int64(len(axis.runs)) * 64); err != nil {
			return nil, err
		}
		prefix := printGeometryPrefix{}
		position := 0
		for _, run := range axis.runs {
			prefix = addPrintGeometryLength(prefix, run, int(run.end)-position)
			axis.prefix = append(axis.prefix, prefix)
			position = int(run.end)
		}
	}
	b.index.bytes = b.allocated + 4096
	if b.index.bytes > formats.PrintGeometryBytes {
		return nil, errors.New("打印几何索引超过内存容量，未发布部分结果")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &b.index, nil
}

func effectivePrintGeometry(spec, fallback printAxisSpec) printGeometryRun {
	if spec.hiddenSet && spec.hidden {
		return printGeometryRun{}
	}
	if !spec.hiddenSet && fallback.hiddenSet && fallback.hidden {
		return printGeometryRun{}
	}
	if spec.status == "invalid" || spec.status == "unresolved" {
		if spec.missingDefault {
			return printGeometryRun{flags: 4}
		}
		return printGeometryRun{flags: 1}
	}
	if spec.size == "" {
		spec = fallback
	}
	if spec.status != "stored" && spec.status != "approximate" {
		if spec.missingDefault {
			return printGeometryRun{flags: 4}
		}
		return printGeometryRun{flags: 1}
	}
	flags := uint32(0)
	if spec.status == "approximate" {
		flags = 2
	}
	return printGeometryRun{units: spec.units, flags: flags}
}

func printGeometryMeasure(raw, unit string, digit float64) (int64, string, string) {
	text := strings.TrimSpace(raw)
	invalid := "源尺寸无效，原值保留"
	if len(text) == 0 || len(text) > formats.PrintGeometryMeasureBytes {
		return 0, "invalid", invalid
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || strings.ContainsAny(text, "xXpP/_") {
		return 0, "invalid", invalid
	}
	// Bound exponent before big.Rat parsing, including values that underflow
	// ParseFloat to zero without an error. No unbounded decimal expansion.
	if at := strings.IndexAny(text, "eE"); at >= 0 {
		exponent, err := strconv.Atoi(text[at+1:])
		if err != nil || exponent < -400 || exponent > 400 {
			return 0, "unresolved", "源尺寸指数超过计算容量，原值保留"
		}
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok || value.Sign() < 0 {
		return 0, "invalid", invalid
	}
	status, reason := "stored", ""
	switch unit {
	case "twip":
		value.Quo(value, big.NewRat(20, 1))
	case "point":
	case "stored-char", "base-char", "biff-col256":
		if value.Sign() == 0 {
			return 0, "stored", ""
		}
		if unit == "biff-col256" {
			number /= 256
		}
		if !(digit > 0 && digit <= 100) {
			return 0, "unresolved", "源列宽缺少可靠的字体测量"
		}
		pixels := math.Floor((256*number + math.Floor(128/digit)) / 256 * digit)
		if unit == "base-char" {
			pixels = math.Floor(number*digit + 5)
		}
		if number == 0 {
			pixels = 0
		}
		if pixels == 0 && value.Sign() > 0 {
			pixels = 1
		}
		if math.IsInf(pixels, 0) || pixels > float64(math.MaxInt64)/float64(formats.PrintGeometryUnitsPerPoint) {
			return 0, "unresolved", "源列宽超过物理计算容量，原值保留"
		}
		value.SetFloat64(pixels * 0.75)
		status, reason = "approximate", "列宽按Normal字体96DPI数字宽度换算，打印机及字体替换可能不同"
	default:
		return 0, "unresolved", "源尺寸单位未解释"
	}
	value.Mul(value, big.NewRat(formats.PrintGeometryUnitsPerPoint, 1))
	integer, remainder := new(big.Int), new(big.Int)
	integer.QuoRem(value.Num(), value.Denom(), remainder)
	if remainder.Sign() != 0 {
		integer.Add(integer, big.NewInt(1))
		status, reason = "approximate", "尺寸向上量化至纳磅，原始尺寸保留"
	}
	if !integer.IsInt64() {
		return 0, "unresolved", "源尺寸超过物理计算容量，原值保留"
	}
	return integer.Int64(), status, reason
}
