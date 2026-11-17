package reconcile

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func independentCSVState(record *Record, columns []int) [6]string {
	kinds, states := []string{}, []string{}
	exists, missing, typeMissing, truncated := []bool{}, []bool{}, []bool{}, []bool{}
	for _, col := range columns {
		v := record.Values[col]
		kinds, states = append(kinds, v.Type), append(states, v.Uncertain)
		exists, missing = append(exists, v.Exists), append(missing, v.MissingCache)
		typeMissing, truncated = append(typeMissing, v.TypeMissing), append(truncated, v.Truncated)
	}
	var out [6]string
	for i, value := range []any{kinds, exists, missing, typeMissing, truncated, states} {
		data, _ := json.Marshal(value)
		out[i] = string(data)
	}
	return out
}

func TestRelationCSVMetadataCacheChecksFullStateDespiteHashCollision(t *testing.T) {
	columns := []int{3, 1, 2}
	record := &Record{Number: 7, Values: map[int]Value{1: text("001"), 2: number("9007199254740993.31"), 3: {Type: "未知\"类型", Exists: true, MissingCache: true, TypeMissing: true, Truncated: true, Uncertain: "状态\n漢字😀"}}}
	var cache relationCSVStateCache
	first := cache.encode(record, columns)
	if first != independentCSVState(record, columns) {
		t.Fatal(first)
	}
	oldEntry := cache.entries[relationCSVStateHash(record, columns)]
	v := record.Values[3]
	v.Exists, v.MissingCache, v.TypeMissing, v.Truncated, v.Type, v.Uncertain = false, false, false, false, "另一个类型", "读取失败"
	record.Values[3] = v
	// Inject a collision, not a production hash hook. A digest match alone
	// must never reuse another row's uncertainty or type flags.
	cache.entries[relationCSVStateHash(record, columns)] = oldEntry
	got := cache.encode(record, columns)
	if got != independentCSVState(record, columns) || got == first {
		t.Fatal("hash collision substituted an earlier state", got)
	}
}

func TestRelationCSVMetadataCacheAdmitsDistinctChineseStatesWithSharedLeadingByte(t *testing.T) {
	var cache relationCSVStateCache
	record := &Record{Values: map[int]Value{}}
	for _, state := range []string{"一", "丁", "一", "丁"} {
		record.Values[1] = Value{Type: "s", Exists: true, Uncertain: state}
		if got := cache.encode(record, []int{1}); got != independentCSVState(record, []int{1}) {
			t.Fatal(got)
		}
	}
	if len(cache.entries) != 2 {
		t.Fatal("common Chinese states unnecessarily bypassed the cache", len(cache.entries))
	}
}

func TestRelationCSVMetadataCacheHasIndependentEntryAndByteBounds(t *testing.T) {
	for _, stateBytes := range []int{10, 20000, formats.RelationCSVCacheBytes + 1} {
		t.Run(fmt.Sprint(stateBytes), func(t *testing.T) {
			var cache relationCSVStateCache
			record := &Record{Values: map[int]Value{}}
			for i := 0; i < 200; i++ {
				record.Values[1] = Value{Raw: strings.Repeat("raw-value", 1000), Type: "s", Exists: true, Uncertain: fmt.Sprint(i) + strings.Repeat("s", stateBytes)}
				if got := cache.encode(record, []int{1}); got != independentCSVState(record, []int{1}) {
					t.Fatal("capacity refusal lost a real state", i)
				}
				if len(cache.entries) > formats.RelationCSVCacheEntries || cache.bytes > formats.RelationCSVCacheBytes {
					t.Fatal("cache retained unbounded metadata", len(cache.entries), cache.bytes)
				}
			}
			if stateBytes > formats.RelationCSVCacheBytes && len(cache.entries) != 0 {
				t.Fatal("oversized state was retained")
			}
		})
	}
}

func TestRelationCSVReusedSlotsNeverLeakValuesIntoAnAbsentSideOrRetainRawText(t *testing.T) {
	plan, row := relationCSVBenchmarkPlan(t, 2)
	var dst bytes.Buffer
	w, err := NewRelationCSVWriter(&dst, plan, func(s string) string {
		if strings.HasPrefix(s, "=") {
			return "'" + s
		}
		return s
	})
	if err != nil {
		t.Fatal(err)
	}
	v := row.Right.Values[2]
	v.Raw = "=SUM(1,2)\n<原值>😀"
	row.Right.Values[2] = v
	if err := w.Write(row); err != nil {
		t.Fatal(err)
	}
	row.Sequence, row.Status, row.RightMatches, row.Right = 2, "unmatched", 0, nil
	if err := w.Write(row); err != nil || w.Flush() != nil {
		t.Fatal(err)
	}
	for _, value := range w.row {
		if value != "" {
			t.Fatal("writer retained a borrowed raw cell after Write")
		}
	}
	rows, err := csv.NewReader(&dst).ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	for i, name := range rows[0] {
		if strings.HasPrefix(name, "右 B ") {
			if rows[1][i] != "'"+v.Raw || rows[2][i] != "" {
				t.Fatal("protected or absent value changed", rows[1][i], rows[2][i])
			}
		}
		if name == "右读取类型(JSON)" && rows[2][i] != "" {
			t.Fatal("absent side inherited cached type flags", rows[2][i])
		}
	}
}

func FuzzRelationCSVMetadataState(f *testing.F) {
	f.Add([]byte("n,s,<state>😀\n"), uint8(15))
	f.Add([]byte{0, 255, 1, 254}, uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, flags uint8) {
		if len(data) > 1024 {
			return
		}
		var cache relationCSVStateCache
		record := &Record{Values: map[int]Value{}}
		for _, text := range []string{"", string(data), "other", string(data), ""} {
			record.Values[1] = Value{Type: text, Uncertain: text, Exists: flags&1 != 0, MissingCache: flags&2 != 0, TypeMissing: flags&4 != 0, Truncated: flags&8 != 0}
			if got := cache.encode(record, []int{1}); got != independentCSVState(record, []int{1}) {
				t.Fatal("cached state differs from independent JSON encoding", got)
			}
		}
	})
}
