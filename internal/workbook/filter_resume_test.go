package workbook

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func TestFilterResumeSourceRecordsHeadersAliasesAndMissingPrefixCache(t *testing.T) {
	w, idx, sourceHash := searchResumeNative(t, 5000)
	rules := []FilterRule{{Column: 1, Operator: "contains", Value: "needle"}, {Column: 2, Operator: "gt", Value: "9007199254740993.30"}}
	opts := FilterOptions{HeaderRow: 1}
	stop := errors.New("pause after a confirmed source record")
	var saved FilterCheckpoint
	var positions []int
	_, err := w.FilterSheetResumable(context.Background(), idx, rules, opts, FilterCheckpoint{}, func(state FilterCheckpoint, added []int) error {
		saved = state
		positions = append(positions, added...)
		added[0] = 4999 // callback aliases must not alter a later delta
		return stop
	}, nil)
	if !errors.Is(err, stop) || saved.Next != formats.CheckpointFilterRows || saved.Complete || saved.Matched != 3 || !reflect.DeepEqual(positions, []int{0, 100, 3000}) {
		t.Fatal("invalid complete-record checkpoint", saved, positions, err)
	}
	if _, cached := w.cachedColumns(idx, []int{1}); cached {
		t.Fatal("interrupted scan published a partial column cache")
	}
	// Core resume must not rescan the old prefix. The server separately binds
	// the source and cache stamps, so this deliberate corruption stays local.
	cacheBytes, err := os.ReadFile(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]byte{}, cacheBytes...)
	copy(changed[idx.RowAt(0).Start:idx.RowAt(0).End], strings.Repeat("!", int(idx.RowAt(0).End-idx.RowAt(0).Start)))
	if err := os.WriteFile(idx.XMLPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := w.FilterSheetResumable(context.Background(), idx, rules, opts, saved, func(_ FilterCheckpoint, added []int) error { positions = append(positions, added...); return nil }, nil)
	if err != nil || !state.Complete || state.Next != 5001 || state.Matched != 4 || !reflect.DeepEqual(positions, []int{0, 100, 3000, 4500}) {
		t.Fatal("resume rescanned or changed the source prefix", state, positions, err)
	}
	if _, cached := w.cachedColumns(idx, []int{1}); cached {
		t.Fatal("resumed suffix became a whole column cache")
	}
	if err := os.WriteFile(idx.XMLPath, cacheBytes, 0600); err != nil {
		t.Fatal(err)
	}
	ordinary, err := w.FilterSheetWithOptions(idx, rules, opts, nil, nil)
	if err != nil || !reflect.DeepEqual(positions, ordinary) {
		t.Fatal("ordinary filter lost prefix values after resume", ordinary, err)
	}
	after, _ := os.ReadFile(w.Path)
	if sha256.Sum256(after) != sourceHash {
		t.Fatal("filter recovery changed the source")
	}
}

func TestFilterResumeTextAdaptersKeepLogicalRecordsAndExactSourceOrder(t *testing.T) {
	for _, format := range []string{"csv", "tsv", "txt", "xls"} {
		t.Run(format, func(t *testing.T) {
			isolateTestStorage(t)
			separator := ","
			if format == "tsv" || format == "txt" {
				separator = "\t"
			}
			file := filepath.Join(t.TempDir(), "data."+format)
			var body strings.Builder
			if format == "xls" {
				body.WriteString("<table><tr><td>编号</td><td>金额</td></tr>")
			} else {
				fmt.Fprintf(&body, "编号%s金额\n", separator)
			}
			for i := 1; i <= 5000; i++ {
				value := fmt.Sprintf("%08d", i)
				if i == 100 || i == 4500 {
					value = "needle"
				}
				if format == "xls" {
					fmt.Fprintf(&body, "<tr><td>%s</td><td>9007199254740993.31</td></tr>", value)
				} else if i == 3000 {
					fmt.Fprintf(&body, "\"two\nlines\"%s9007199254740993.31\n", separator)
				} else {
					fmt.Fprintf(&body, "%s%s9007199254740993.31\n", value, separator)
				}
			}
			if format == "xls" {
				body.WriteString("</table>")
			}
			if err := os.WriteFile(file, []byte(body.String()), 0600); err != nil {
				t.Fatal(err)
			}
			w, err := OpenWorkbook(file)
			if err != nil {
				t.Fatal(err)
			}
			idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			var positions []int
			var saved FilterCheckpoint
			pause := errors.New("pause")
			rules := []FilterRule{{Column: 1, Operator: "contains", Value: "needle"}}
			_, err = w.FilterSheetResumable(context.Background(), idx, rules, FilterOptions{HeaderRow: 1}, FilterCheckpoint{}, func(state FilterCheckpoint, delta []int) error {
				saved = state
				positions = append(positions, delta...)
				return pause
			}, nil)
			if !errors.Is(err, pause) || saved.Next != 4096 {
				t.Fatal(saved, err)
			}
			final, err := w.FilterSheetResumable(context.Background(), idx, rules, FilterOptions{HeaderRow: 1}, saved, func(_ FilterCheckpoint, delta []int) error { positions = append(positions, delta...); return nil }, nil)
			if err != nil || !final.Complete || idx.RowCount() != 5001 || !reflect.DeepEqual(positions, []int{0, 100, 4500}) {
				t.Fatal("logical records or source coordinates changed", final, positions, err)
			}
		})
	}
}

func TestFilterSortStageUsesExistingExactTypedNaturalAndHeaderRules(t *testing.T) {
	values := []string{"金额", "2", "10", "9007199254740993.31", "9007199254740993.30", "(2.00)", "￥1,234.50", "25%", "0.25", "abc", "", "0002"}
	var lines []string
	for i, value := range values {
		lines = append(lines, fmt.Sprintf(`<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s</t></is></c></row>`, i+1, i+1, value))
	}
	idx := syntheticIndex(t, lines...)
	w := &Workbook{CacheDir: t.TempDir()}
	filtered, err := NewRowOrder(context.Background(), filterOrderDirectory(w), idx.RowCount(), []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11})
	if err != nil {
		t.Fatal(err)
	}
	defer filtered.Close()
	for _, tc := range []struct {
		mode string
		desc bool
		want []int
	}{{"", false, []int{0, 5, 8, 11, 2, 1, 7, 4, 3, 9, 6, 10}}, {"number", false, []int{0, 5, 7, 8, 1, 11, 2, 6, 4, 3, 9, 10}}, {"number", true, []int{0, 3, 4, 6, 2, 1, 11, 7, 8, 5, 9, 10}}, {"natural", false, nil}} {
		opts := FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 1, Mode: tc.mode, Descending: tc.desc}}}
		got, err := w.SortFilteredRowOrder(context.Background(), idx, filtered, opts, nil)
		if err != nil {
			t.Fatal(tc, err)
		}
		positions, err := got.Page(0, got.Len())
		_ = got.Close()
		if err != nil || tc.want != nil && !reflect.DeepEqual(positions, tc.want) {
			t.Fatal("sorting phase changed established semantics", tc, positions, err)
		}
		ordinary, err := w.FilterSheetWithOptions(idx, nil, opts, nil, nil)
		if err != nil || !reflect.DeepEqual(positions, ordinary) {
			t.Fatal("sorting phase differs from original one-pass execution", tc, positions, ordinary, err)
		}
	}
}

func TestFilterResumeInvalidCursorCancellationAndSaveFailureDoNotPublishColumns(t *testing.T) {
	w, idx, _ := searchResumeNative(t, 5000)
	rules := []FilterRule{{Column: 1, Operator: "notBlank"}}
	for _, state := range []FilterCheckpoint{{Next: -1}, {Next: idx.RowCount() + 1}, {Next: 5, Matched: 6}, {Next: 5, Complete: true}} {
		if _, err := w.FilterSheetResumable(context.Background(), idx, rules, FilterOptions{}, state, nil, nil); err == nil {
			t.Fatal("invalid source cursor was accepted", state)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.FilterSheetResumable(ctx, idx, rules, FilterOptions{}, FilterCheckpoint{}, nil, nil); err != context.Canceled {
		t.Fatal(err)
	}
	failure := errors.New("disk confirmation failed")
	if _, err := w.FilterSheetResumable(context.Background(), idx, rules, FilterOptions{}, FilterCheckpoint{}, func(FilterCheckpoint, []int) error { return failure }, nil); err != failure {
		t.Fatal(err)
	}
	if _, cached := w.cachedColumns(idx, []int{1}); cached {
		t.Fatal("unconfirmed source scan published columns")
	}
}
