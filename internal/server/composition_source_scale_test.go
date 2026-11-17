package server

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"excellens/internal/composition"
)

func writeCompositionScaleCSV(t *testing.T, file string, rows int) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	_, err = fmt.Fprintln(w, "编号,金额")
	for i := 1; i <= rows && err == nil; i++ {
		_, err = fmt.Fprintf(w, "%010d,9007199254740993.31\n", i)
	}
	if flushErr := w.Flush(); err == nil {
		err = flushErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompositionActualMillionCSVStreamsEveryExactValueAndSourceCoordinate(t *testing.T) {
	if os.Getenv("EXCELLENS_COMPOSITION_SCALE") != "1" {
		t.Skip("set EXCELLENS_COMPOSITION_SCALE=1 for the actual million-row probe")
	}
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "million.csv")
	writeCompositionScaleCSV(t, file, 1_000_000)
	before := printScaleHash(t, file)
	a, _ := recordOpen(t, file)
	snapshot := compositionCapture(t, a, compositionDefinition(t, a))
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	peak := memory.Alloc
	started := time.Now()
	var count int64
	a.cacheMu.RLock()
	err := snapshot.scan(context.Background(), []int{1, 2}, func(row composition.Record) error {
		count++
		if row.Sequence != count {
			return fmt.Errorf("result order changed at %d", count)
		}
		if row.Input == 0 {
			if row.Row != int(count)+1 || row.Position != int(count) || row.Cells[0].Data.Raw != fmt.Sprintf("%010d", count) || row.Cells[1].Data.Raw != "9007199254740993.31" || row.Cells[1].Data.Ref != fmt.Sprintf("B%d", count+1) {
				return fmt.Errorf("source value or coordinate changed at %d", count)
			}
		}
		if count&4095 == 0 {
			runtime.ReadMemStats(&memory)
			peak = max(peak, memory.Alloc)
		}
		return nil
	})
	a.cacheMu.RUnlock()
	if err != nil || count != 1_000_002 || snapshot.data.plan.Rows() != count || printScaleHash(t, file) != before {
		t.Fatal("actual million-row append was incomplete or modified source", count, err)
	}
	t.Logf("actual CSV source rows=1000000; union rows=%d; all raw amounts and coordinates checked; scanMs=%d; sampledGoHeapBytes=%d; sourceSHA256=%s", count, time.Since(started).Milliseconds(), peak, before)
}

func TestCompositionActualFullBIFFStreamsEveryNativeValueAndBoundaryRow(t *testing.T) {
	if os.Getenv("EXCELLENS_COMPOSITION_SCALE") != "1" {
		t.Skip("set EXCELLENS_COMPOSITION_SCALE=1 for the full physical BIFF8 probe")
	}
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "full.xls")
	printBIFFSource(t, file, 65_536)
	before := printScaleHash(t, file)
	a, _ := recordOpen(t, file)
	snapshot := compositionCapture(t, a, compositionDefinition(t, a))
	var count int64
	a.cacheMu.RLock()
	err := snapshot.scan(context.Background(), []int{1, 2}, func(row composition.Record) error {
		count++
		if row.Sequence != count {
			return fmt.Errorf("BIFF result order changed at %d", count)
		}
		if row.Input == 0 && (row.Row != int(count)+1 || row.Cells[0].Data.Raw != fmt.Sprintf("%010d", row.Row) || row.Cells[0].Data.Type != "inlineStr" || row.Cells[1].Data.Raw != strconv.Itoa(row.Row)+".5" || row.Cells[1].Data.Type != "n") {
			return fmt.Errorf("native BIFF value/type/coordinate changed at %d", count)
		}
		return nil
	})
	a.cacheMu.RUnlock()
	if err != nil || count != 65_537 || printScaleHash(t, file) != before {
		t.Fatal("full physical BIFF append changed source or lost records", count, err)
	}
	t.Logf("actual BIFF source rows=65536; header excluded; union rows=%d; every native type/value/coordinate and last row checked; sourceSHA256=%s", count, before)
}
