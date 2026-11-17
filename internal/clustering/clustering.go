// Package clustering discovers read-only text candidates. A common key is a
// reason to ask a person, never an instruction to merge business identities.
package clustering

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type Options struct {
	FoldCase          bool   `json:"foldCase"`
	FoldWidth         bool   `json:"foldWidth"`
	Space             string `json:"space"`
	IgnorePunctuation bool   `json:"ignorePunctuation"`
}

func (o Options) Validate() error {
	switch o.Space {
	case "", "trim", "collapse", "remove":
		return nil
	default:
		return errors.New("空白比较规则无效")
	}
}

// Key does not tokenize, transliterate, parse amounts or discard accents.
func (o Options) Key(ctx context.Context, raw string) (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	if len(raw) > formats.ClusterTextBytes || !utf8.ValidString(raw) {
		return "", errors.New("聚类原文无效或超过128KiB，未截取原文")
	}
	var b strings.Builder
	b.Grow(len(raw))
	space := false
	for at, ch := range raw {
		if at&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		if o.FoldWidth {
			switch {
			case ch >= 0xff01 && ch <= 0xff5e:
				ch -= 0xfee0
			case ch == 0x3000:
				ch = ' '
			}
		}
		if o.IgnorePunctuation && unicode.IsPunct(ch) {
			continue
		}
		if (o.Space == "collapse" || o.Space == "remove") && unicode.IsSpace(ch) {
			space = o.Space == "collapse" && b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(ch)
	}
	key := b.String()
	if o.Space == "trim" {
		key = strings.TrimSpace(key)
	}
	if o.FoldCase {
		key = workbook.FoldComparisonText(key)
	}
	return key, ctx.Err()
}

type Record struct {
	Row     int
	Cell    workbook.CellData
	Numeric bool // the existing source interpretation, never inferred here
}
type Scan func(context.Context, func(Record) error) error
type Progress func(stage string, done, total int64)

type Variant struct {
	Raw          string `json:"raw"`
	Type         string `json:"type"`
	Count        int64  `json:"count"`
	FormulaCount int64  `json:"formulaCount"`
	Rows         []int  `json:"rows"`
}
type Group struct {
	ID       int       `json:"id"`
	Key      string    `json:"key"`
	Kind     string    `json:"kind"`
	Count    int64     `json:"count"`
	Variants []Variant `json:"variants"`
}
type Counts struct {
	Scanned       int64 `json:"scanned"`
	Eligible      int64 `json:"eligible"`
	Blank         int64 `json:"blank"`
	MissingCache  int64 `json:"missingCache"`
	Numeric       int64 `json:"numeric"`
	OtherTypes    int64 `json:"otherTypes"`
	EmptyKey      int64 `json:"emptyKey"`
	Unique        int64 `json:"unique"`
	ExactGroups   int64 `json:"exactGroups"`
	SimilarGroups int64 `json:"similarGroups"`
}
type Result struct {
	Options Options `json:"options"`
	Counts  Counts  `json:"counts"`
	Groups  []Group `json:"groups"`
	bytes   int64
}

func (r *Result) BudgetBytes() int64 {
	if r == nil {
		return 0
	}
	return r.bytes
}

func TextType(kind string) bool {
	return kind == "s" || kind == "str" || kind == "inlineStr"
}

type limits struct {
	groups, variants int
	bytes, sortBytes int64
}

func Build(ctx context.Context, dir string, total int, scan Scan, options Options, progress Progress) (*Result, error) {
	return build(ctx, dir, total, scan, options, progress, limits{
		formats.ClusterGroups, formats.ClusterVariants, formats.ClusterResultBytes, formats.QuerySortMemoryBytes,
	})
}

// Hex segments preserve arbitrary NULs, separators and Unicode without key
// collisions. Source coordinates travel in verified sort records; no full
// reverse row map or second source scan is constructed.
func build(ctx context.Context, dir string, total int, scan Scan, options Options, progress Progress, limit limits) (*Result, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if scan == nil || total < 0 || total > formats.ExternalRows {
		return nil, errors.New("聚类来源记录数无效")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &Result{Options: options, Groups: []Group{}, bytes: 512}
	var group Group
	var currentVariant *Variant
	var variantIdentity, groupIdentity, firstRaw string
	var blockBytes int64
	var outputVariants int
	var differentRaw bool
	finish := func() error {
		if group.Count < 2 {
			return nil
		}
		if len(result.Groups) >= limit.groups || outputVariants+len(group.Variants) > limit.variants || result.bytes+blockBytes > limit.bytes {
			return errors.New("聚类候选超过结果容量，请缩小范围或采用更严格的规则；未发布部分结果")
		}
		group.ID = len(result.Groups)
		group.Kind = "exact"
		if differentRaw {
			group.Kind = "similar"
			result.Counts.SimilarGroups++
		} else {
			result.Counts.ExactGroups++
		}
		result.Groups = append(result.Groups, group)
		outputVariants += len(group.Variants)
		result.bytes += blockBytes
		return nil
	}
	err := workbook.SortKeyStream(ctx, dir, total, limit.sortBytes, func(ctx context.Context, add func(workbook.KeyRecord) error) error {
		err := scan(ctx, func(record Record) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			position := int(result.Counts.Scanned)
			if position >= total || record.Row < 1 || record.Row > formats.ExternalRows {
				return errors.New("聚类来源记录数或源坐标与快照不一致")
			}
			result.Counts.Scanned++
			cell := record.Cell
			switch {
			case cell.CacheMissing:
				result.Counts.MissingCache++
			case cell.Raw == "":
				result.Counts.Blank++
			case record.Numeric:
				result.Counts.Numeric++
			case !TextType(cell.Type):
				result.Counts.OtherTypes++
			default:
				normalized, err := options.Key(ctx, cell.Raw)
				if err != nil {
					return fmt.Errorf("源行%d：%w", record.Row, err)
				}
				if normalized == "" {
					result.Counts.EmptyKey++
					break
				}
				formula := "0"
				if cell.HasFormula {
					formula = "1"
				}
				key := hex.EncodeToString([]byte(normalized)) + "/" + hex.EncodeToString([]byte(cell.Type)) + "/" +
					hex.EncodeToString([]byte(cell.Raw)) + "/" + fmt.Sprintf("%010d", record.Row) + "/" + formula
				if err := add(workbook.KeyRecord{Position: position, Key: key}); err != nil {
					return err
				}
				result.Counts.Eligible++
			}
			if progress != nil && result.Counts.Scanned&4095 == 0 {
				progress("读取聚类原文", result.Counts.Scanned, int64(total))
			}
			return nil
		})
		if err == nil && result.Counts.Scanned != int64(total) {
			err = errors.New("聚类来源记录数与快照不一致")
		}
		return err
	}, func(record workbook.KeyRecord) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		parts := strings.Split(record.Key, "/")
		if len(parts) != 5 {
			return errors.New("聚类排序记录结构无效")
		}
		row, err := strconv.Atoi(parts[3])
		if err != nil || row < 1 || row > formats.ExternalRows || (parts[4] != "0" && parts[4] != "1") {
			return errors.New("聚类排序记录坐标或公式状态无效")
		}
		if parts[0] != groupIdentity {
			if err := finish(); err != nil {
				return err
			}
			key, err := hex.DecodeString(parts[0])
			if err != nil {
				return err
			}
			group = Group{Key: string(key), Variants: []Variant{}}
			blockBytes = int64(len(key)) + 256
			groupIdentity = strings.Clone(parts[0])
			variantIdentity, currentVariant, firstRaw, differentRaw = "", nil, "", false
		}
		identity := parts[1] + "/" + parts[2]
		if identity != variantIdentity {
			if len(group.Variants) >= limit.variants {
				return errors.New("单个聚类候选原文种类超过容量，未发布部分结果")
			}
			raw, err := hex.DecodeString(parts[2])
			if err != nil {
				return err
			}
			kind, err := hex.DecodeString(parts[1])
			if err != nil || !TextType(string(kind)) {
				return errors.New("聚类排序记录读取类型无效")
			}
			blockBytes += int64(len(raw)+len(kind)+formats.ClusterSampleRows*8) + 512
			if blockBytes > limit.bytes {
				return errors.New("单个聚类候选超过文字容量，未发布部分结果")
			}
			text := string(raw)
			if len(group.Variants) == 0 {
				firstRaw = text
			} else if firstRaw != text {
				differentRaw = true
			}
			group.Variants = append(group.Variants, Variant{Raw: text, Type: string(kind), Rows: make([]int, 0, formats.ClusterSampleRows)})
			currentVariant = &group.Variants[len(group.Variants)-1]
			variantIdentity = identity
			result.Counts.Unique++
		}
		currentVariant.Count++
		if parts[4] == "1" {
			currentVariant.FormulaCount++
		}
		if len(currentVariant.Rows) < formats.ClusterSampleRows {
			currentVariant.Rows = append(currentVariant.Rows, row)
		}
		group.Count++
		return nil
	}, func(stage string, done, total int) {
		if progress != nil {
			progress(stage, int64(done), int64(total))
		}
	})
	if err == nil {
		err = finish()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
