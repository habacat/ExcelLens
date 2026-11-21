package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"math/big"
	"sort"
	"strings"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

var errStop = errors.New("reconciliation stream consumer stopped")

type keyedRecord struct {
	key    string
	reason string
	record Record
}

func NeededColumns(query Query, right bool) []int {
	set := make(map[int]bool)
	for _, pairs := range [][]Pair{query.Keys, query.Fields} {
		for _, pair := range pairs {
			column := pair.Left
			if right {
				column = pair.Right
			}
			set[column] = true
		}
	}
	columns := make([]int, 0, len(set))
	for column := range set {
		columns = append(columns, column)
	}
	sort.Ints(columns)
	return columns
}

func checkedKey(record Record, query Query, right bool) (string, string, error) {
	if record.Position < 0 || record.Number < 1 || record.Number > formats.ExternalRows {
		return "", "", errors.New("核对源行坐标无效")
	}
	var bytes int64
	for _, value := range record.Values {
		bytes += int64(len(value.Raw))
		if value.Display != value.Raw {
			bytes += int64(len(value.Display))
		}
	}
	if bytes > MaxRecordBytes {
		return "", "", fmt.Errorf("源行%d所选核对字段超过8MiB限制，请减少比较字段", record.Number)
	}
	key, reason := recordKey(record, query.Keys, right, query.Rules())
	if reason != "" {
		key = invalidRecordKey(record.Position, right)
	}
	return key, reason, nil
}

func invalidRecordKey(position int, right bool) string {
	var raw [6]byte
	raw[1] = 'L'
	if right {
		raw[1] = 'R'
	}
	binary.BigEndian.PutUint32(raw[2:], uint32(position))
	return string(raw[:]) // invalid/empty keys never match each other
}

// Pulling one source retains at most its current key heads and row reader.
// Both sources share the per-query 64MiB sort budget, split equally. Iterator
// cancellation unwinds every reader and run even during the other source scan.
func sortedRecords(ctx context.Context, dir string, source Source, query Query, right bool, progress Progress) iter.Seq2[keyedRecord, error] {
	return func(yield func(keyedRecord, error) bool) {
		columns := NeededColumns(query, right)
		label := "左侧"
		if right {
			label = "右侧"
		}
		var streamErr error
		var expected workbook.KeyRecord
		var scanned, read int
		lastReport := time.Now()
		positions := func(emit func(int) bool) {
			streamErr = workbook.SortKeyStream(ctx, dir, source.SourceRows, formats.QuerySortMemoryBytes/2,
				func(ctx context.Context, add func(workbook.KeyRecord) error) error {
					err := source.Scan(ctx, columns, func(record Record) error {
						if err := ctx.Err(); err != nil {
							return err
						}
						if record.Position < 0 || record.Position >= source.SourceRows || record.Number < 1 {
							return errors.New("核对源行坐标无效")
						}
						key, _, err := checkedKey(record, query, right)
						if err != nil {
							return err
						}
						if err := add(workbook.KeyRecord{Position: record.Position, Key: key}); err != nil {
							return err
						}
						scanned++
						if progress != nil && (scanned == source.Info.Total || scanned&4095 == 0 && time.Since(lastReport) > 200*time.Millisecond) {
							progress("读取"+label+"主键", int64(scanned), int64(source.Info.Total))
							lastReport = time.Now()
						}
						return nil
					})
					if err == nil && scanned != source.Info.Total {
						err = errors.New("核对来源记录数与快照不一致")
					}
					return err
				}, func(record workbook.KeyRecord) error {
					expected = record
					if !emit(record.Position) {
						return errStop
					}
					return nil
				}, func(stage string, done, total int) {
					if progress != nil {
						progress(label+"："+stage, int64(done), int64(total))
					}
				})
		}
		err := source.Read(ctx, iter.Seq[int](positions), columns, func(record Record) error {
			if record.Position != expected.Position {
				return errors.New("核对行序与读取来源不一致")
			}
			key, reason, err := checkedKey(record, query, right)
			if err != nil {
				return err
			}
			if key != expected.Key {
				return errors.New("核对来源在读取期间发生变化，请重新运行")
			}
			read++
			if !yield(keyedRecord{key: key, reason: reason, record: record}, nil) {
				return errStop
			}
			return nil
		})
		if errors.Is(err, errStop) {
			return
		}
		if err == nil && streamErr != nil && !errors.Is(streamErr, errStop) {
			err = streamErr
		}
		if err == nil && (read != scanned || ctx.Err() != nil) {
			err = ctx.Err()
			if err == nil {
				err = errors.New("核对来源未完整读取")
			}
		}
		if err != nil {
			yield(keyedRecord{}, err)
		}
	}
}

func ownRecord(record Record) *Record {
	copy := record
	copy.Values = make(map[int]Value, len(record.Values))
	for column, value := range record.Values {
		value.Raw = strings.Clone(value.Raw)
		if value.Display == record.Values[column].Raw {
			value.Display = value.Raw
		} else {
			value.Display = strings.Clone(value.Display)
		}
		value.Type = strings.Clone(value.Type)
		value.Uncertain = strings.Clone(value.Uncertain)
		copy.Values[column] = value
	}
	return &copy
}

func Run(ctx context.Context, dir string, query Query, left, right Source, sinks Sinks, progress Progress) (Result, error) {
	if err := Validate(query, left, right); err != nil {
		return Result{}, err
	}
	tolerances := make([]*big.Rat, len(query.Fields))
	for i, pair := range query.Fields {
		tolerances[i], _ = pair.AmountTolerance()
	} // checked by Validate above
	result := Result{Left: left.Info, Right: right.Info, Query: query, Samples: []Group{}}
	nextLeft, stopLeft := iter.Pull2(sortedRecords(ctx, dir, left, query, false, progress))
	defer stopLeft()
	nextRight, stopRight := iter.Pull2(sortedRecords(ctx, dir, right, query, true, progress))
	defer stopRight()
	l, err, hasLeft := nextLeft()
	if err != nil {
		return Result{}, err
	}
	r, err, hasRight := nextRight()
	if err != nil {
		return Result{}, err
	}
	sampleBytes := 0
	lastReport := time.Now()
	for hasLeft || hasRight {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		key := l.key
		if !hasLeft || hasRight && r.key < key {
			key = r.key
		}
		digest := sha256.Sum256([]byte(key))
		group := Group{KeyID: fmt.Sprintf("%x", digest[:16])}
		duplicateStarted := false
		writeDuplicate := func(side string, record Record) error {
			if sinks.Duplicate != nil {
				return sinks.Duplicate(group.KeyID, side, record)
			}
			return nil
		}
		consume := func(row keyedRecord, isRight bool) error {
			side := "left"
			var first *Record
			if isRight {
				side = "right"
				group.RightCount++
				result.Counts.RightRecords++
				if group.RightCount == 1 {
					group.Right = ownRecord(row.record)
				}
				first = group.Right
				if len(group.RightRows) < 3 {
					group.RightRows = append(group.RightRows, row.record.Number)
				}
			} else {
				group.LeftCount++
				result.Counts.LeftRecords++
				if group.LeftCount == 1 {
					group.Left = ownRecord(row.record)
				}
				first = group.Left
				if len(group.LeftRows) < 3 {
					group.LeftRows = append(group.LeftRows, row.record.Number)
				}
			}
			if group.Key == nil {
				for _, pair := range query.Keys {
					column := pair.Left
					if isRight {
						column = pair.Right
					}
					group.Key = append(group.Key, first.Values[column].Raw)
				}
			}
			if row.reason != "" {
				group.Reason = row.reason
			}
			done := result.Counts.LeftRecords + result.Counts.RightRecords
			if progress != nil && done&4095 == 0 && time.Since(lastReport) > 250*time.Millisecond {
				progress("核对记录", done, int64(left.Info.Total)+int64(right.Info.Total))
				lastReport = time.Now()
			}
			if duplicateStarted {
				return writeDuplicate(side, row.record)
			}
			if group.LeftCount > 1 || group.RightCount > 1 {
				duplicateStarted = true
				if group.Left != nil {
					if err := writeDuplicate("left", *group.Left); err != nil {
						return err
					}
				}
				if group.Right != nil {
					if err := writeDuplicate("right", *group.Right); err != nil {
						return err
					}
				}
				return writeDuplicate(side, row.record)
			}
			return nil
		}
		for hasLeft && l.key == key {
			if err := consume(l, false); err != nil {
				return Result{}, err
			}
			l, err, hasLeft = nextLeft()
			if err != nil {
				return Result{}, err
			}
		}
		for hasRight && r.key == key {
			if err := consume(r, true); err != nil {
				return Result{}, err
			}
			r, err, hasRight = nextRight()
			if err != nil {
				return Result{}, err
			}
		}
		switch {
		case key[0] == 0:
			group.Status = "invalid_key"
			result.Counts.InvalidKeys += group.LeftCount + group.RightCount
		case duplicateStarted:
			group.Status, group.Reason = "duplicate", "主键存在重复记录，未自动配对"
			result.Counts.Duplicate++
		case group.LeftCount == 0:
			group.Status = "right_only"
			result.Counts.RightOnly++
		case group.RightCount == 0:
			group.Status = "left_only"
			result.Counts.LeftOnly++
		default:
			group.Status = "equal"
			for i, pair := range query.Fields {
				comparison := compareValues(group.Left.Values[pair.Left], group.Right.Values[pair.Right], pair, i, query.Rules(), tolerances[i])
				group.Comparisons = append(group.Comparisons, comparison)
				if comparison.Reason != "" {
					group.Status = "review"
				} else if comparison.WithinTolerance {
					if group.Status == "equal" {
						group.Status = "within_tolerance"
					}
				} else if !comparison.Equal && group.Status != "review" {
					group.Status = "different"
				}
			}
			switch group.Status {
			case "within_tolerance":
				result.Counts.WithinTolerance++
			case "equal":
				result.Counts.Equal++
			case "different":
				result.Counts.Different++
			case "review":
				result.Counts.Review++
			}
		}
		if query.IncludeEqual || group.Status != "equal" {
			if sinks.Group != nil {
				if err := sinks.Group(group); err != nil {
					return Result{}, err
				}
			}
			if !result.SamplesTruncated && len(result.Samples) < MaxSamples && sampleBytes < MaxSampleBytes {
				sample, bytes := sampleGroup(group)
				if sampleBytes+bytes <= MaxSampleBytes {
					result.Samples = append(result.Samples, sample)
					sampleBytes += bytes
				} else {
					result.SamplesTruncated = true
				}
			} else {
				result.SamplesTruncated = true
			}
		}
		done, total := result.Counts.LeftRecords+result.Counts.RightRecords, int64(left.Info.Total)+int64(right.Info.Total)
		if progress != nil && (done == total || time.Since(lastReport) > 250*time.Millisecond) {
			progress("核对记录", done, total)
			lastReport = time.Now()
		}
	}
	if result.Counts.LeftRecords != int64(left.Info.Total) || result.Counts.RightRecords != int64(right.Info.Total) {
		return Result{}, errors.New("两侧核对记录数与来源快照不一致")
	}
	return result, ctx.Err()
}
