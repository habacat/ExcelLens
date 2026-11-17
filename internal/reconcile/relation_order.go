package reconcile

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"iter"
	"slices"
	"sort"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

var relationModes = [...]string{"left", "inner", "left_matched", "left_unmatched", "right_unmatched", "left_invalid", "right_invalid"}

// Pair positions and a typed key digest are independent of source values.
// The fixed encoding is only used inside disposable, CRC-checked sort runs.
type relationPair struct {
	positions  [2]int
	keyHash    [32]byte
	status     string
	rightCount int64
}

func compareRelationPairs(a, b relationPair) int {
	if result := cmp.Compare(a.positions[0], b.positions[0]); result != 0 {
		return result
	}
	return cmp.Compare(a.positions[1], b.positions[1])
}

type relationCandidates struct{ rows []relationPair }

func (c *relationCandidates) add(pair relationPair) bool {
	limit := formats.RelationPreviewRows
	if len(c.rows) == limit && compareRelationPairs(pair, c.rows[limit-1]) >= 0 {
		return false // later pairs in this source-ordered group cannot qualify
	}
	at := sort.Search(len(c.rows), func(i int) bool { return compareRelationPairs(c.rows[i], pair) >= 0 })
	if len(c.rows) < limit {
		c.rows = append(c.rows, relationPair{})
	}
	copy(c.rows[at+1:], c.rows[at:len(c.rows)-1])
	c.rows[at] = pair
	return true
}

var errRelationGroupComplete = errors.New("relation group candidate prefix complete")

// A group's equal-key positions are already in original source order. Only
// its first N pairs can belong to the first N pairs of the complete result.
// Rejecting the first noncompetitive candidate skips the remaining product.
func (p *Relation) groupPairs(ctx context.Context, orders [2]*workbook.RowOrder, group relationGroup, mode string, limit int, visit func(relationPair) error) error {
	invalid, l, r := group.key[0] == 0, group.count[0], group.count[1]
	keyHash := sha256.Sum256([]byte(group.key))
	count := 0
	emit := func(li, ri int, status string) error {
		if limit > 0 && count >= limit {
			return errRelationGroupComplete
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		pair := relationPair{positions: [2]int{-1, -1}, keyHash: keyHash, status: status}
		if status == "matched" {
			pair.rightCount = int64(r)
		}
		for side, offset := range []int{li, ri} {
			if offset >= 0 {
				pair.positions[side] = orders[side].At(group.start[side] + offset)
			}
		}
		if err := visit(pair); err != nil {
			return err
		}
		count++
		return nil
	}
	switch {
	case invalid:
		if l > 0 && (mode == "left" || mode == "left_invalid") {
			return emit(0, -1, "invalid_key")
		}
		if r > 0 && mode == "right_invalid" {
			return emit(-1, 0, "invalid_key")
		}
	case l > 0 && r > 0:
		if mode == "left" || mode == "inner" || mode == "left_matched" {
			for li := 0; li < l; li++ {
				if mode == "left_matched" {
					if err := emit(li, -1, "matched"); err != nil {
						return err
					}
					continue
				}
				for ri := 0; ri < r; ri++ {
					if err := emit(li, ri, "matched"); err != nil {
						return err
					}
				}
			}
		}
	case l > 0:
		if mode == "left" || mode == "left_unmatched" {
			for li := 0; li < l; li++ {
				if err := emit(li, -1, "unmatched"); err != nil {
					return err
				}
			}
		}
	case r > 0:
		if mode == "right_unmatched" {
			for ri := 0; ri < r; ri++ {
				if err := emit(-1, ri, "unmatched"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (p *Relation) collectPreviews(ctx context.Context, group relationGroup, candidates *[len(relationModes)]relationCandidates) error {
	for i, mode := range relationModes {
		err := p.groupPairs(ctx, p.orders, group, mode, formats.RelationPreviewRows, func(pair relationPair) error {
			if !candidates[i].add(pair) {
				return errRelationGroupComplete
			}
			return nil
		})
		if err != nil && !errors.Is(err, errRelationGroupComplete) {
			return err
		}
	}
	return ctx.Err()
}

func (p *Relation) sortedPairs(ctx context.Context, orders [2]*workbook.RowOrder, mode string, total int64, budget int64) iter.Seq2[relationPair, error] {
	return func(yield func(relationPair, error) bool) {
		if total == 0 {
			return
		}
		err := workbook.SortKeyStream(ctx, p.dir, int(total), budget, func(ctx context.Context, add func(workbook.KeyRecord) error) error {
			var count int64
			err := p.groups(ctx, orders, func(group relationGroup) error {
				return p.groupPairs(ctx, orders, group, mode, 0, func(pair relationPair) error {
					if count >= total {
						return errors.New("关联配对数量超过完整预检")
					}
					var raw [45]byte
					binary.BigEndian.PutUint32(raw[:], uint32(pair.positions[0]+1))
					binary.BigEndian.PutUint32(raw[4:], uint32(pair.positions[1]+1))
					switch pair.status {
					case "matched":
						raw[8] = 1
					case "unmatched":
						raw[8] = 2
					case "invalid_key":
						raw[8] = 3
					}
					binary.BigEndian.PutUint32(raw[9:], uint32(pair.rightCount))
					copy(raw[13:], pair.keyHash[:])
					if err := add(workbook.KeyRecord{Position: int(count), Key: string(raw[:])}); err != nil {
						return err
					}
					count++
					return nil
				})
			})
			if err == nil && count != total {
				err = errors.New("关联配对数量与完整预检不一致")
			}
			return err
		}, func(record workbook.KeyRecord) error {
			pair, err := p.decodePair(record.Key)
			if err != nil {
				return err
			}
			if !yield(pair, nil) {
				return errStop
			}
			return nil
		}, nil)
		if err != nil && !errors.Is(err, errStop) {
			yield(relationPair{}, err)
		}
	}
}

func (p *Relation) decodePair(encoded string) (relationPair, error) {
	var pair relationPair
	if len(encoded) != 45 {
		return pair, errors.New("关联原序配对记录不完整")
	}
	for side, at := range []int{0, 4} {
		position := int(binary.BigEndian.Uint32([]byte(encoded[at:at+4]))) - 1
		if position < -1 || position >= p.sources[side].SourceRows {
			return pair, errors.New("关联原序源位置无效")
		}
		pair.positions[side] = position
	}
	if pair.positions == [2]int{-1, -1} {
		return pair, errors.New("关联原序记录没有来源")
	}
	switch encoded[8] {
	case 1:
		pair.status = "matched"
	case 2:
		pair.status = "unmatched"
	case 3:
		pair.status = "invalid_key"
	default:
		return pair, errors.New("关联原序记录状态无效")
	}
	pair.rightCount = int64(binary.BigEndian.Uint32([]byte(encoded[9:13])))
	if pair.rightCount > int64(p.sources[1].Info.Total) {
		return pair, errors.New("关联原序匹配数无效")
	}
	if pair.status == "matched" {
		if pair.positions[0] < 0 || pair.rightCount == 0 {
			return pair, errors.New("关联原序匹配记录缺少左来源或匹配数")
		}
	} else if pair.rightCount != 0 || pair.positions[0] >= 0 && pair.positions[1] >= 0 {
		return pair, errors.New("关联原序未匹配状态与来源或数量不一致")
	}
	copy(pair.keyHash[:], encoded[13:])
	return pair, nil
}

func (p *Relation) previewPairs(mode string) iter.Seq2[relationPair, error] {
	return func(yield func(relationPair, error) bool) {
		index := slices.Index(relationModes[:], mode)
		if index < 0 {
			yield(relationPair{}, errors.New("关联预览方式无效"))
			return
		}
		for _, pair := range p.previews[index] {
			if !yield(pair, nil) {
				return
			}
		}
	}
}
