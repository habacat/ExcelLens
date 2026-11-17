package reconcile

import (
	"archive/zip"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func relationCSVBenchmarkPlan(b testing.TB, columns int) (*Relation, RelationRow) {
	b.Helper()
	values := make([]Value, columns)
	selected := make([]int, columns)
	for i := range columns {
		values[i] = Value{Raw: "9007199254740993.31", Type: "n", Exists: true}
		selected[i] = i + 1
	}
	values[0] = text("000123")
	left, right := relationFixture(values), relationFixture(values)
	for i, source := range []*Source{&left, &right} {
		source.Info.Name = fmt.Sprintf("来源%d_关联数据.xlsx", i)
		source.Info.ID, source.Info.Fingerprint, source.Info.Stamp = strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	}
	q := RelationQuery{Keys: []Pair{{Left: 1, Right: 1}}, LeftColumns: selected, RightColumns: selected}
	p, err := PrepareRelation(context.Background(), b.TempDir(), q, left, right, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = p.Close() })
	var row RelationRow
	if _, err := p.Stream(context.Background(), "inner", 1, func(r RelationRow) error {
		row = RelationRow{Sequence: 1, Status: r.Status, RightMatches: r.RightMatches}
		l, rcopy := ownRecord(*r.Left), ownRecord(*r.Right)
		row.Left, row.Right = l, rcopy
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	return p, row
}

func BenchmarkRelationCSVWriter(b *testing.B) {
	for _, columns := range []int{2, 64} {
		for _, diverse := range []bool{false, true} {
			b.Run(fmt.Sprintf("columns%d/diverse%t", columns, diverse), func(b *testing.B) {
				plan, row := relationCSVBenchmarkPlan(b, columns)
				w, err := NewRelationCSVWriter(io.Discard, plan, nil)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					row.Sequence = w.Rows + 1
					if diverse {
						v := row.Left.Values[2]
						v.Uncertain = fmt.Sprintf("状态%d", row.Sequence%128)
						row.Left.Values[2] = v
					}
					if err := w.Write(row); err != nil {
						b.Fatal(err)
					}
				}
				if err := w.Flush(); err != nil {
					b.Fatal(err)
				}
			})
		}
	}
}

// This isolates serialization and standard DEFLATE costs using synthetic
// complete relation rows. It is not an adapter, whole-app or RSS benchmark.
func TestRelationCSVCompressionProfile(t *testing.T) {
	if os.Getenv("EXCELLENS_RELATION_EXPORT_PROFILE") != "1" {
		t.Skip("optional synthetic relation CSV/compression profile")
	}
	plan, row := relationCSVBenchmarkPlan(t, 2)
	const records = 100_000
	var cases []map[string]any
	var originalHash string
	for _, level := range []int{0, flate.BestSpeed, 3, flate.DefaultCompression} {
		f, err := os.Create(filepath.Join(t.TempDir(), fmt.Sprintf("level%d", level)))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		var dst io.Writer = f
		var zw *zip.Writer
		if level != 0 {
			zw = zip.NewWriter(f)
			zw.RegisterCompressor(zip.Deflate, func(dst io.Writer) (io.WriteCloser, error) { return flate.NewWriter(dst, level) })
			dst, err = zw.Create("关联结果.csv")
			if err != nil {
				t.Fatal(err)
			}
		}
		started := time.Now()
		w, err := NewRelationCSVWriter(io.MultiWriter(dst, h), plan, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < records; i++ {
			row.Sequence, row.Left.Number, row.Right.Number = int64(i+1), i+2, i+2
			if err := w.Write(row); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if zw != nil {
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		info, err := f.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		digest := fmt.Sprintf("%x", h.Sum(nil))
		if level == 0 {
			originalHash = digest
		} else if digest != originalHash {
			t.Fatal("compression changed logical CSV bytes")
		}
		cases = append(cases, map[string]any{"level": level, "records": records, "elapsedMs": time.Since(started).Milliseconds(), "outputBytes": info.Size(), "logicalCSV_SHA256": digest})
	}
	if path := os.Getenv("EXCELLENS_RELATION_EXPORT_PROFILE_RECEIPT"); path != "" {
		data, _ := json.MarshalIndent(map[string]any{"result": "PASS", "scope": "synthetic complete-row serialization and standard compression", "cases": cases}, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("synthetic serialization/compression profile: %+v", cases)
}
