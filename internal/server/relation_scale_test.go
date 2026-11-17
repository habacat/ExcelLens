package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

func TestRelationActualMillionCSVAndFullBIFFPreflightAndCompleteExtraction(t *testing.T) {
	if os.Getenv("EXCELLENS_RELATION_SCALE") != "1" {
		t.Skip("set EXCELLENS_RELATION_SCALE=1 for actual complete source checks")
	}
	for _, kind := range []string{"csv", "xls"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			dir := t.TempDir()
			file := filepath.Join(dir, "source."+kind)
			first, last, expected := 1, 1_000_000, int64(1_000_000)
			if kind == "csv" {
				writeCompositionScaleCSV(t, file, 1_000_000)
			} else {
				printBIFFSource(t, file, 65_536)
				first, last, expected = 2, 65_536, 65_535
			}
			other := filepath.Join(dir, "lookup.csv")
			content := fmt.Sprintf("编号,内容\n%010d,last\n%010d,first\n%010d,last again\nmissing,no\n", last, first, last)
			if err := os.WriteFile(other, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			a, meta := recordOpen(t, file)
			b, otherMeta := recordOpen(t, other)
			book, otherBook := a.currentWorkbook(), b.currentWorkbook()
			before, otherBefore := printScaleHash(t, file), printScaleHash(t, other)
			left := relationServerSource(t, a, book, "wb:"+meta.Name, 1, 2)
			right := relationServerSource(t, b, otherBook, "wb:"+otherMeta.Name, 1, 2)
			started := time.Now()
			queryDir := filepath.Join(book.CacheDir, "queries")
			p, err := reconcile.PrepareRelation(context.Background(), queryDir, relationServerQuery(), left, right, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Close() })
			counts := p.Counts()
			if counts.LeftRecords != expected || counts.RightRecords != 4 || counts.InnerRows != 3 || counts.LeftJoinRows != expected+1 || counts.LeftOnly != expected-2 || counts.RightOnly != 1 || counts.RightDuplicateGroups != 1 {
				t.Fatal(counts)
			}
			preview, err := p.Preview(context.Background(), "left")
			if err != nil || len(preview.Rows) != 100 || !preview.Truncated || preview.Rows[0].Left.Values[1].Raw != fmt.Sprintf("%010d", first) {
				t.Fatal("bounded exact preview failed", len(preview.Rows), err)
			}
			var complete, lastCopies int64
			previousLeft, previousRight := -1, -1
			_, err = p.Stream(context.Background(), "left", expected+1, func(row reconcile.RelationRow) error {
				complete++
				if row.Left == nil || row.Left.Number != row.Left.Position+1 || row.Sequence != complete {
					return fmt.Errorf("wrong source coordinates at %d", complete)
				}
				position := row.Left.Position
				rightPosition := -1
				if row.Right != nil {
					rightPosition = row.Right.Position
				}
				if position < previousLeft || position == previousLeft && rightPosition <= previousRight {
					return fmt.Errorf("original source order changed at %d: %d/%d after %d/%d", complete, position, rightPosition, previousLeft, previousRight)
				}
				previousLeft, previousRight = position, rightPosition
				if complete <= int64(len(preview.Rows)) && !reflect.DeepEqual(row, preview.Rows[complete-1]) {
					return fmt.Errorf("preview differs from complete original-order prefix at %d", complete)
				}
				identity, amount := int64(position), "9007199254740993.31"
				if kind == "xls" {
					identity, amount = int64(row.Left.Number), strconv.Itoa(row.Left.Number)+".5"
					if row.Left.Values[2].Type != "n" || row.Left.Values[1].Type != "inlineStr" {
						return fmt.Errorf("BIFF type changed at %d", complete)
					}
				}
				if row.Left.Values[1].Raw != fmt.Sprintf("%010d", identity) || row.Left.Values[2].Raw != amount {
					return fmt.Errorf("raw identity/amount changed at %d", complete)
				}
				if identity == int64(last) {
					lastCopies++
					if row.Right == nil || row.RightMatches != 2 || row.Right.Number != int(lastCopies)*2 {
						return fmt.Errorf("duplicate lookup was not fully expanded")
					}
				}
				return nil
			})
			if err != nil || complete != expected+1 || lastCopies != 2 {
				t.Fatal("incomplete physical extraction", complete, lastCopies, err)
			}
			if err := p.Close(); err != nil || workbook.QueryFilesInUse(queryDir) {
				t.Fatal("closed relation retained temporary files", err)
			}
			if printScaleHash(t, file) != before || printScaleHash(t, other) != otherBefore {
				t.Fatal("relation changed a source")
			}
			t.Logf("actual %s: source=%d; left join=%d; preview=100; every original coordinate/value and left/right output order checked; preview equals full prefix; duplicate final key=2 in right source order; source hashes unchanged; elapsed=%s", kind, expected, complete, time.Since(started))
		})
	}
}
