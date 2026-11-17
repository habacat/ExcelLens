package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"excellens/internal/composition"
	"excellens/internal/formats"
)

func TestCompositionTaskActualMillionAndFullBIFFUseBoundedPreviewAndCompleteBorrowedSource(t *testing.T) {
	if os.Getenv("EXCELLENS_COMPOSITION_SCALE") != "1" {
		t.Skip("set EXCELLENS_COMPOSITION_SCALE=1 for the actual API scale probes")
	}
	for _, kind := range []string{"csv", "xls"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), "source."+kind)
			var expected int64
			if kind == "csv" {
				writeCompositionScaleCSV(t, file, 1_000_000)
				expected = 1_000_002
			} else {
				printBIFFSource(t, file, 65_536)
				expected = 65_537
			}
			before := printScaleHash(t, file)
			a, _ := recordOpen(t, file)
			definition := compositionDefinition(t, a)
			ref := definition.Inputs[0].Ref
			fields := compositionStart(t, a, compositionStartRequest{Mode: "fields", Choice: &compositionChoice{Book: ref.Book, Source: ref.Source, HeaderRow: 1}})
			waitComposition(t, a, fields, "done")
			if result := compositionResult(t, a, fields); result.Rows != expected-2 {
				t.Fatal("field task lost exact indexed record count", result.Rows)
			}
			job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition, KeepID: fields.ID})
			waitComposition(t, a, job, "done")
			result := compositionResult(t, a, job)
			if result.Rows != expected || len(result.Preview) != formats.CompositionPreviewRows || !result.PreviewTruncated || result.Preview[0].Row != 2 || result.Preview[0].Cells[1].Ref != "B2" || result.Preview[99].Row != 101 {
				t.Fatal("API scale preview widened, truncated its source or changed source coordinates")
			}
			a.cacheMu.RLock()
			source, err := a.borrowCompositionTask(job.ID, a.currentWorkbook())
			a.cacheMu.RUnlock()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(source.close)
			clearComposition(t, a, fields.ID)
			clearComposition(t, a, job.ID)
			if compositionUsed(a) != source.data.bytes {
				t.Fatal("cleared API result did not leave exactly its consumer's source reservation")
			}
			var count int64
			a.cacheMu.RLock()
			err = source.scan(context.Background(), []int{1, 2}, func(row composition.Record) error {
				count++
				if row.Sequence != count {
					return fmt.Errorf("union sequence changed at %d", count)
				}
				if row.Input != 0 {
					return nil
				}
				if row.Row != int(count)+1 || row.Position != int(count) {
					return fmt.Errorf("source coordinate changed at %d", count)
				}
				number, amount := count, "9007199254740993.31"
				if kind == "xls" {
					number, amount = int64(row.Row), strconv.Itoa(row.Row)+".5"
					if row.Cells[1].Data.Type != "n" || row.Cells[0].Data.Type != "inlineStr" {
						return fmt.Errorf("BIFF source types changed at %d", count)
					}
				}
				if row.Cells[0].Data.Raw != fmt.Sprintf("%010d", number) || row.Cells[1].Data.Raw != amount {
					return fmt.Errorf("exact source values changed at %d", count)
				}
				return nil
			})
			a.cacheMu.RUnlock()
			source.close()
			if err != nil || count != expected || compositionUsed(a) != 0 || printScaleHash(t, file) != before {
				t.Fatal("API source became incomplete after owner clear or leaked data/budget", count, err, compositionUsed(a))
			}
			t.Logf("actual %s API: indexed records=%d; preview=%d; borrowed complete rows=%d; every input value/type/coordinate checked; final retained bytes=0; sourceSHA256=%s", kind, expected-2, len(result.Preview), count, before)
		})
	}
}
