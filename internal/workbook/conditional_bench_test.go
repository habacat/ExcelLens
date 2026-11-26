package workbook

import (
	"context"
	"path/filepath"
	"testing"
)

// Measure the per-cell evaluator separately from XML decoding and disk reads.
func BenchmarkConditionalCellEvaluation(b *testing.B) {
	dir := b.TempDir()
	for _, key := range []string{"LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		b.Setenv(key, dir)
	}
	ctx := context.Background()
	w, err := OpenWorkbook(filepath.Join("testdata", "conditional-native", "1900.xlsx"))
	if err != nil {
		b.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	rules, err := w.SourceConditionalFormats(ctx, w.Sheets[0], idx)
	if err != nil {
		b.Fatal(err)
	}
	eval := rules.NewEvaluator(ctx)
	resolve := func(row, col int) (CellData, bool, error) { return CellData{Raw: "6", Type: "n"}, true, nil }
	b.ReportAllocs()
	for b.Loop() {
		eval.engine.ResetBudget()
		result, err := eval.Evaluate(3, 7, CellData{Raw: "1", Type: "n"}, resolve)
		if err != nil || result == nil || result.Matched != 1 || result.Notice != "" {
			b.Fatal(result, err)
		}
	}
}
