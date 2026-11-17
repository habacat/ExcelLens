//go:build windows

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/reconcile"
)

func TestRelationWindowsSQLiteAndGB18030KeepOriginalUncertainty(t *testing.T) {
	for _, name := range []string{"ledger.db", "accounting-gbk.txt"} {
		t.Run(name, func(t *testing.T) {
			isolateTestStorage(t)
			original, err := os.ReadFile(filepath.Join("..", "workbook", "testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(file, original, 0600); err != nil {
				t.Fatal(err)
			}
			a, meta := recordOpen(t, file)
			book := a.currentWorkbook()
			if name == "ledger.db" {
				for _, candidate := range book.Sheets {
					if strings.HasPrefix(candidate.Name, "财务") {
						meta = candidate
						break
					}
				}
			}
			q := relationServerQuery()
			columns := []int{1, 2}
			if name == "ledger.db" {
				q.LeftColumns, q.RightColumns = []int{1, 2, 3, 4}, []int{1, 2, 3, 4}
				columns = q.LeftColumns
			}
			source := relationServerSource(t, a, book, "wb:"+meta.Name, columns...)
			p, err := reconcile.PrepareRelation(context.Background(), filepath.Join(book.CacheDir, "queries"), q, source, source, nil)
			if err != nil {
				t.Fatal(err)
			}
			if name == "ledger.db" {
				if p.Counts().LeftInvalid != int64(source.Info.Total) || p.Counts().InnerRows != 0 || p.Counts().LeftOnly != 0 {
					t.Fatal("unretained SQLite types became reliable matches/unmatched", p.Counts())
				}
				p.Close()
				q.Keys[0].Mode = "text"
				p, err = reconcile.PrepareRelation(context.Background(), filepath.Join(book.CacheDir, "queries"), q, source, source, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			preview, err := p.Preview(context.Background(), "inner")
			if err != nil || len(preview.Rows) == 0 {
				t.Fatal(preview, err)
			}
			if name == "ledger.db" {
				var row *reconcile.Record
				for _, candidate := range preview.Rows {
					if candidate.Left.Values[1].Raw == "0000000123456789" {
						row = candidate.Left
					}
				}
				if row == nil {
					t.Fatal("SQLite original identifier missing", preview)
				}
				if row.Values[1].Raw != "0000000123456789" || row.Values[2].Raw != "100000000000.123456" || row.Values[3].Raw != "[NULL]" || row.Values[4].Raw != "[BLOB 3 bytes]" || !row.Values[1].TypeMissing || row.Values[3].Uncertain == "" || row.Values[4].Uncertain == "" {
					t.Fatal("SQLite states were reinterpreted", row)
				}
				p.Close()
				q.Keys = []reconcile.Pair{{Left: 3, Right: 3, Mode: "text"}}
				p, err = reconcile.PrepareRelation(context.Background(), filepath.Join(book.CacheDir, "queries"), q, source, source, nil)
				if err != nil || p.Counts().LeftInvalid == 0 || p.Counts().LeftOnly != 0 {
					t.Fatal("ambiguous placeholder paired as reliable text", err)
				}
			} else {
				found := false
				for _, row := range preview.Rows {
					if row.Left.Values[1].Raw == "现金" {
						found = true
						if row.Right.Values[1].Raw != "现金" {
							t.Fatal(row)
						}
					}
				}
				if !found {
					t.Fatal("GB18030 original text lost", preview)
				}
			}
			p.Close()
			after, err := os.ReadFile(file)
			if err != nil || string(after) != string(original) {
				t.Fatal("relation changed original source", err)
			}
		})
	}
}
