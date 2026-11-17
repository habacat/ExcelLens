//go:build windows

package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportResumeWindowsSQLiteAndGB18030Adapters(t *testing.T) {
	for _, name := range []string{"ledger.db", "accounting-gbk.txt"} {
		t.Run(name, func(t *testing.T) {
			isolateTestStorage(t)
			data, err := os.ReadFile(filepath.Join("..", "workbook", "testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			exportResumeCompareAdapter(t, file)
		})
	}
}
