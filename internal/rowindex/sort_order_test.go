package rowindex

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func sortFixture(t *testing.T, rows int, keys []SortKey, budget int64) (*RowSorter, *QueryQuota) {
	t.Helper()
	quota := &QueryQuota{max: 16 << 20}
	s, err := NewRowSorter(filepath.Join(t.TempDir(), "queries"), rows, keys, budget, quota)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Abort)
	return s, quota
}

func testParseSortNumber(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	neg := false
	if len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		neg = true
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	pct := strings.HasSuffix(s, "%")
	if pct {
		s = strings.TrimSpace(strings.TrimSuffix(s, "%"))
	}
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, "￥", "")
	s = strings.ReplaceAll(s, "¥", "")
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, "元", "")
	s = strings.ReplaceAll(s, "人民币", "")
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	if neg {
		v = -v
	}
	if pct {
		v /= 100
	}
	return v, true
}

func makeTestSortValue(text, mode string) SortValue {
	var n float64
	var numeric bool
	if mode == "number" {
		n, numeric = testParseSortNumber(text)
	}
	key := SortValue{Text: strings.Clone(text), Number: n, IsNumber: numeric}
	if !numeric {
		key.Folded = strings.ToLower(key.Text)
	}
	return key
}

func addSortFixture(t *testing.T, s *RowSorter, rows int) {
	t.Helper()
	amounts := []string{"9007199254740993.31", "9007199254740993.30", "57%", "0.57", "(2.00)", "￥1,234.50", "0002", "", "NaN", "1e-300"}
	for i := 0; i < rows; i++ {
		values := make([]SortValue, len(s.keys))
		for k, key := range s.keys {
			text := amounts[(i*17+k*3)%len(amounts)]
			if k == 1 {
				text = fmt.Sprintf("项目%03d段%d", i%13, i%7)
			}
			if k == 2 {
				text = []string{"Ａ", "中文", "abc", "ABC", "K", "k", "", "ｚ"}[i%8]
			}
			values[k] = makeTestSortValue(text, key.Mode)
		}
		if err := s.Add(context.Background(), i, values); err != nil {
			t.Fatal(err)
		}
		managed := int64(cap(s.items))*(sortItemMemory+int64(len(s.keys))*sortValueMemory) + s.stringBytes
		if managed > s.budget {
			t.Fatalf("comparison key storage exceeded budget: %d > %d", managed, s.budget)
		}
	}
}

func TestExternalSortMultiPassMatchesResidentAndPublishesOneRowOrder(t *testing.T) {
	const rows = 1009
	keys := []SortKey{{Column: 1, Mode: "number"}, {Column: 2, Mode: "natural", Descending: true}, {Column: 3, Mode: "text"}, {Column: 4, Mode: "number", Descending: true}, {Column: 5}}
	resident, _ := sortFixture(t, rows, keys, 8<<20)
	external, quota := sortFixture(t, rows, keys, 32<<10)
	addSortFixture(t, resident, rows)
	addSortFixture(t, external, rows)
	if len(resident.runs) != 0 || len(external.runs) <= sortMergeFanIn {
		t.Fatalf("fixture did not exercise resident and multiple merge passes: %d / %d", len(resident.runs), len(external.runs))
	}
	var want []int
	if err := resident.Finish(context.Background(), func(i int) error { want = append(want, i); return nil }, nil); err != nil {
		t.Fatal(err)
	}
	b, err := NewRowOrderBuilder(external.dir, rows, 16, quota)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	if err := external.Finish(context.Background(), func(i int) error { return b.Add(context.Background(), i) }, nil); err != nil {
		t.Fatal(err)
	}
	order, err := b.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if order.data.mapping == nil || order.Len() != rows {
		t.Fatal("result did not use the mapped row order")
	}
	for visual, source := range want {
		if order.At(visual) != source {
			t.Fatalf("merge output %d: %d want %d", visual, order.At(visual), source)
		}
		if got, ok := order.Position(source); !ok || got != visual {
			t.Fatalf("reverse address %d = %d / %v", source, got, ok)
		}
	}
	external.Abort()
	if err := order.Close(); err != nil {
		t.Fatal(err)
	}
	if quota.used.Load() != 0 || len(quota.files) != 0 {
		t.Fatal("completed sort leaked temporary storage", quota.used.Load(), len(quota.files))
	}
}

func TestExternalSortRejectsDamagedRunsAndReleasesAllReaders(t *testing.T) {
	for _, damage := range []string{"header", "payload", "truncated", "oversized-record"} {
		t.Run(damage, func(t *testing.T) {
			s, quota := sortFixture(t, 300, []SortKey{{Column: 1, Mode: "number"}}, 32<<10)
			addSortFixture(t, s, 300)
			if err := s.spill(context.Background()); err != nil {
				t.Fatal(err)
			}
			run := s.runs[0]
			data, err := os.ReadFile(run.file.path)
			if err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "header":
				data[20] ^= 1
			case "payload":
				data[len(data)-1] ^= 1
			case "truncated":
				data = data[:len(data)-1]
			case "oversized-record":
				binary.LittleEndian.PutUint32(data[sortRunHeader:], ^uint32(0))
			}
			if err := os.WriteFile(run.file.path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := s.Finish(context.Background(), func(int) error { return nil }, nil); err == nil {
				t.Fatal("damaged sort was accepted")
			}
			s.Abort()
			if quota.used.Load() != 0 || len(quota.files) != 0 {
				t.Fatal("failed sort retained disk or open handles")
			}
		})
	}
}

func TestExternalSortCancellationQuotaAndOversizedKeys(t *testing.T) {
	for _, failure := range []string{"cancel-before-sort", "cancel-during-merge", "disk-budget", "output-failure", "long-key", "duplicate-source"} {
		t.Run(failure, func(t *testing.T) {
			s, quota := sortFixture(t, 1000, []SortKey{{Column: 1, Mode: "number"}}, 32<<10)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var err error
			switch failure {
			case "disk-budget":
				quota.max = 100
				for i := 0; i < 1000 && err == nil; i++ {
					err = s.Add(ctx, i, []SortValue{makeTestSortValue(fmt.Sprint(1000-i), "number")})
				}
			case "long-key":
				err = s.Add(ctx, 0, []SortValue{makeTestSortValue(strings.Repeat("x", int(s.maxRecord)), "text")})
			case "duplicate-source":
				if err := s.Add(ctx, 0, []SortValue{{Text: "x", Folded: "x"}}); err != nil {
					t.Fatal(err)
				}
				err = s.Add(ctx, 0, []SortValue{{Text: "x", Folded: "x"}})
			default:
				addSortFixture(t, s, 1000)
				if len(s.runs) == 0 {
					t.Fatal("fixture did not spill")
				}
				if failure == "cancel-before-sort" {
					cancel()
				}
				emitted := 0
				err = s.Finish(ctx, func(int) error {
					emitted++
					if emitted == 17 {
						if failure == "output-failure" {
							return errors.New("destination write failed")
						}
						cancel()
					}
					return nil
				}, nil)
			}
			if err == nil {
				t.Fatal("failure was silently accepted")
			}
			if strings.HasPrefix(failure, "cancel-") && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost its state", err)
			}
			s.Abort()
			if quota.used.Load() != 0 || len(quota.files) != 0 {
				t.Fatal("failed sort leaked temporary files")
			}
		})
	}
}

type comparatorCancelContext struct {
	context.Context
	checks int
}

func (c *comparatorCancelContext) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	return nil
}

func TestResidentSortCanCancelWithinComparator(t *testing.T) {
	s, _ := sortFixture(t, 10000, []SortKey{{Column: 1, Mode: "text"}}, 8<<20)
	for i := 0; i < 10000; i++ {
		value := fmt.Sprintf("item%d", i*7919%10000)
		if err := s.Add(context.Background(), i, []SortValue{makeTestSortValue(value, "text")}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &comparatorCancelContext{Context: context.Background()}
	if err := s.sortChunk(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("comparison cancellation did not propagate", err)
	}
}
