package workbook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestColumnDictionaryPreservesTypesAndFallsBackForHighCardinality(t *testing.T) {
	const rows = 100000
	v := beginColumnCache(rows)
	for i := 0; i < rows; i++ {
		v = writeColumnCache(v, i, CellData{Type: "s", Display: "000123"}, true)
	}
	if v == nil || v.codes == nil || v.bytes > 1<<20 {
		t.Fatal("low-cardinality column not compressed")
	}
	for _, row := range []int{0, 1000, rows - 1} {
		cell, ok := cacheCell(v, row)
		if !ok || cell.Display != "000123" || !isTextCell(cell) {
			t.Fatal("identifier/type changed", cell)
		}
	}
	large := beginColumnCache(10000)
	for i := 0; i < 10000; i++ {
		large = writeColumnCache(large, i, CellData{Type: "n", Display: fmt.Sprintf("%d.123456789", i)}, true)
	}
	if large == nil || large.codes != nil || len(large.values) != 10000 {
		t.Fatal("high-cardinality fallback missing")
	}
	for _, row := range []int{0, 4095, 4096, 9999} {
		cell, ok := cacheCell(large, row)
		if !ok || cell.Display != fmt.Sprintf("%d.123456789", row) || isTextCell(cell) {
			t.Fatal("fallback changed a value/type", cell)
		}
	}
}

func TestConcurrentSameSourceIndexReusesCompletedCache(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("ID@value\n"+strings.Repeat("000123@long-column-value-to-fill-a-block\n", 40000)), 0600); err != nil {
		t.Fatal(err)
	}
	one, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	two, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	first := true
	go func() {
		_, err := one.BuildIndex(one.Sheets[0], func(_, _ int64, _ string) {
			if first {
				first = false
				close(entered)
				<-release
			}
		}, nil)
		finished <- err
	}()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("index stopped before progress", err)
	case <-time.After(5 * time.Second):
		t.Fatal("index did not reach progress boundary")
	}
	secondDone := make(chan error, 1)
	reused := false
	go func() {
		_, err := two.BuildIndex(two.Sheets[0], func(_, _ int64, stage string) { reused = strings.Contains(stage, "缓存") }, nil)
		secondDone <- err
	}()
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil || !reused {
		t.Fatal("second index repeated source scan", reused, err)
	}
	if got := two.GetCachedIndex(two.Sheets[0].Path); got == nil || got.MaxRow != 40001 {
		t.Fatal("shared cache incomplete")
	}
}
