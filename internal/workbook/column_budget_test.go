package workbook

import (
	"fmt"
	"runtime"
	"testing"
)

func TestColumnDictionaryExpansionRejectsBeforeLargeAllocation(t *testing.T) {
	v := beginColumnCache(4_000_000)
	for i := 0; i < 4095; i++ {
		v = writeColumnCache(v, i, CellData{Type: "s", Display: fmt.Sprint(i)}, true)
	}
	if v == nil {
		t.Fatal("dictionary should fit before expansion")
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	v = writeColumnCache(v, 4095, CellData{Type: "s", Display: "new-unique-value"}, true)
	runtime.ReadMemStats(&after)
	if v != nil {
		t.Fatal("over-budget expanded cache was retained")
	}
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 1<<20 {
		t.Fatalf("allocated %d bytes before rejecting an over-budget column", delta)
	}
}
