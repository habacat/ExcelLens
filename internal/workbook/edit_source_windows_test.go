//go:build windows

package workbook

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEditSourceUsesRealWindowsWriteSharing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.csv")
	if err := os.WriteFile(path, []byte("A,B\r\n1,2\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		_ = w.Close()
		_ = read.Close()
		t.Fatal("writer obtained a live edit source")
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	w, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if f, err := OpenEditSource(path); err == nil {
		_ = f.Close()
		t.Fatal("opened edit source over a live writer")
	}
}
