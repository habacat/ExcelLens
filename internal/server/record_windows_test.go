//go:build windows

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordWindowsSQLiteAndGB18030KeepAdapterTypesAndSource(t *testing.T) {
	for _, tc := range []struct{ name, fixture, format string }{
		{"sqlite", "ledger.db", "sqlite"}, {"gb18030", "accounting-gbk.txt", "txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTestStorage(t)
			original, err := os.ReadFile(filepath.Join("..", "workbook", "testdata", tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), tc.fixture)
			if err := os.WriteFile(file, original, 0600); err != nil {
				t.Fatal(err)
			}
			a, meta := recordOpen(t, file)
			if tc.name == "sqlite" {
				for _, candidate := range a.currentWorkbook().Sheets {
					if strings.HasPrefix(candidate.Name, "财务") {
						meta = candidate
						break
					}
				}
				if _, err := a.currentWorkbook().BuildIndex(meta, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if a.currentWorkbook().Format != tc.format {
				t.Fatal(a.currentWorkbook().Format)
			}
			req := recordSource(t, a, meta.Name, rowViewRequest{})
			r := assertRecordCommonRead(t, a, meta, req)
			if tc.name == "sqlite" {
				if r.Fields[0].Cell.Raw != "0000000123456789" || r.Fields[1].Cell.Raw != "100000000000.123456" || r.Fields[2].Cell.Raw != "[NULL]" || r.Fields[3].Cell.Raw != "[BLOB 3 bytes]" {
					t.Fatal("SQLite value states changed", r.Fields)
				}
			} else if r.Fields[0].Cell.Raw != "现金" {
				t.Fatal("GB18030 source changed", r.Fields)
			}
			after, err := os.ReadFile(file)
			if err != nil || string(after) != string(original) {
				t.Fatal("source changed", err)
			}
		})
	}
}
