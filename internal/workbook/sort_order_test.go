package workbook

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func sortFixture(t *testing.T, rows int, keys []SortKey, budget int64) (*rowSorter, *queryQuota) {
	t.Helper()
	quota := &queryQuota{max: 16 << 20}
	s, err := newRowSorter(filepath.Join(t.TempDir(), "queries"), rows, keys, budget, quota)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.abort)
	return s, quota
}

func addSortFixture(t *testing.T, s *rowSorter, rows int) {
	t.Helper()
	amounts := []string{"9007199254740993.31", "9007199254740993.30", "57%", "0.57", "(2.00)", "￥1,234.50", "0002", "", "NaN", "1e-300"}
	for i := 0; i < rows; i++ {
		values := make([]sortValue, len(s.keys))
		for k, key := range s.keys {
			text := amounts[(i*17+k*3)%len(amounts)]
			if k == 1 {
				text = fmt.Sprintf("项目%03d段%d", i%13, i%7)
			}
			if k == 2 {
				text = []string{"Ａ", "中文", "abc", "ABC", "K", "k", "", "ｚ"}[i%8]
			}
			values[k] = makeSortValue(CellData{Type: "s", Raw: text}, key.Mode)
		}
		if err := s.add(context.Background(), i, values); err != nil {
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
	if err := resident.finish(context.Background(), func(i int) error { want = append(want, i); return nil }, nil); err != nil {
		t.Fatal(err)
	}
	b, err := newRowOrderBuilder(external.dir, rows, 16, quota)
	if err != nil {
		t.Fatal(err)
	}
	defer b.abort()
	if err := external.finish(context.Background(), func(i int) error { return b.add(context.Background(), i) }, nil); err != nil {
		t.Fatal(err)
	}
	order, err := b.finish(context.Background())
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
	external.abort()
	if err := order.Close(); err != nil {
		t.Fatal(err)
	}
	if quota.used.Load() != 0 || len(quota.files) != 0 {
		t.Fatal("completed sort leaked temporary storage", quota.used.Load(), len(quota.files))
	}
}

func TestNaturalSortIsExplicitStableAndNeverParsesIntegerRuns(t *testing.T) {
	values := []string{"项目10", "项目2", "项目002", "项目1", "项目2-10", "项目2-2", "", "项目" + strings.Repeat("9", 1000), "项目1" + strings.Repeat("0", 1000)}
	var lines []string
	for i, text := range values {
		lines = append(lines, fmt.Sprintf(`<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s</t></is></c></row>`, i+1, i+1, text))
	}
	idx := syntheticIndex(t, lines...)
	w := &Workbook{CacheDir: t.TempDir()}
	for _, tc := range []struct {
		desc bool
		want []int
	}{
		{false, []int{3, 1, 2, 5, 4, 0, 7, 8, 6}},
		{true, []int{8, 7, 0, 4, 5, 1, 2, 3, 6}},
	} {
		got, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: 1, Mode: "natural", Descending: tc.desc}}}, nil, nil)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("desc=%v: %v / %v want %v", tc.desc, got, err, tc.want)
		}
	}
	plain, err := w.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: 1, Mode: "text"}}}, nil, nil)
	if err != nil || slices.Index(plain, 0) >= slices.Index(plain, 1) {
		t.Fatal("ordinary text mode unexpectedly changed to natural ordering", plain, err)
	}
	for _, pair := range [][2]string{{"file000", "file0"}, {"a0002b0003", "a2b3"}, {"000", "0"}} {
		if compareNaturalText(pair[0], pair[1]) != 0 {
			t.Fatal("leading-zero numeric run was not stable", pair)
		}
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
			if err := s.finish(context.Background(), func(int) error { return nil }, nil); err == nil {
				t.Fatal("damaged sort was accepted")
			}
			s.abort()
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
					err = s.add(ctx, i, []sortValue{makeSortValue(CellData{Raw: fmt.Sprint(1000 - i)}, "number")})
				}
			case "long-key":
				err = s.add(ctx, 0, []sortValue{makeSortValue(CellData{Raw: strings.Repeat("x", int(s.maxRecord))}, "text")})
			case "duplicate-source":
				if err := s.add(ctx, 0, []sortValue{{text: "x", folded: "x"}}); err != nil {
					t.Fatal(err)
				}
				err = s.add(ctx, 0, []sortValue{{text: "x", folded: "x"}})
			default:
				addSortFixture(t, s, 1000)
				if len(s.runs) == 0 {
					t.Fatal("fixture did not spill")
				}
				if failure == "cancel-before-sort" {
					cancel()
				}
				emitted := 0
				err = s.finish(ctx, func(int) error {
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
			s.abort()
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
		if err := s.add(context.Background(), i, []sortValue{makeSortValue(CellData{Raw: value}, "text")}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &comparatorCancelContext{Context: context.Background()}
	if err := s.sortChunk(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("comparison cancellation did not propagate", err)
	}
}
