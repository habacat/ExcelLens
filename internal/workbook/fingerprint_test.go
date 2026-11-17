package workbook

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Same OOXML bytes in differently named and differently compressed archives
// must reuse the *same* index/search/view cache directory.
func TestContentFingerprintIgnoresExternalNameAndZIPCompression(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "audit-2025.xlsx")
	second := filepath.Join(dir, "renamed-copy.xlsx")
	third := filepath.Join(dir, "repacked.xlsx")
	changed := filepath.Join(dir, "changed.xlsx")
	writeTinyWorkbook(t, first)
	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, raw, 0600); err != nil {
		t.Fatal(err)
	}
	repackWorkbook(t, first, third, zip.Store, false)
	repackWorkbook(t, first, changed, zip.Store, true)
	a, err := OpenWorkbook(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := OpenWorkbook(second)
	if err != nil {
		t.Fatal(err)
	}
	c, err := OpenWorkbook(third)
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenWorkbook(changed)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint != b.Fingerprint || a.CacheDir != b.CacheDir {
		t.Fatalf("renamed workbook cache miss %q vs %q", a.Fingerprint, b.Fingerprint)
	}
	if a.Fingerprint != c.Fingerprint || a.CacheDir != c.CacheDir {
		t.Fatalf("repacked workbook cache miss %q vs %q", a.Fingerprint, c.Fingerprint)
	}
	if a.Fingerprint == d.Fingerprint || a.CacheDir == d.CacheDir {
		t.Fatal("changed contents must invalidate all workbook caches")
	}
	meta, _ := a.FindSheet("Data")
	if _, err := a.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	if b.GetCachedIndex(meta.Path) == nil || c.GetCachedIndex(meta.Path) == nil {
		t.Fatal("renamed/repacked workbooks should reuse created on-disk indexes")
	}
}

func repackWorkbook(t *testing.T, src, dest string, method uint16, change bool) {
	t.Helper()
	zr, err := zip.OpenReader(src)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	for _, zf := range zr.File {
		r, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		if change && zf.Name == "xl/worksheets/sheet1.xml" {
			b = append(b, []byte("<!-- changed -->")...)
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: zf.Name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitSourceCheckRejectsChangedWorkbookWithoutDiscardingCachedSnapshot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "source.xlsx")
	writeTinyWorkbook(t, file)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := wb.ReadRows(idx, 0, 2, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []bool{false, true} {
		repacked := filepath.Join(dir, "repacked.xlsx")
		repackWorkbook(t, file, repacked, zip.Store, change)
		data, err := os.ReadFile(repacked)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := wb.CheckSourceUnchanged(); (err != nil) != change {
			t.Fatal("source identity did not track changed ZIP parts", change, err)
		}
	}
	after, err := wb.ReadRows(idx, 0, 2, 1, 3)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("changed live file invalidated pinned cached values", after, err)
	}
}

func TestExplicitSourceBindingDetectsSameSizeTimestampPreservingTextRewrites(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "source.csv")
	if err := os.WriteFile(file, []byte("ID,Amount\nA,123\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := wb.CheckSourceUnchanged(); err != nil {
		t.Fatal("original text fingerprint domain lost", err)
	}
	st, _ := os.Stat(file)
	if err := os.WriteFile(file, []byte("ID,Amount\nA,999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := wb.CheckSourceUnchanged(); err == nil {
		t.Fatal("source identity trusted size and timestamp after contents changed")
	}
}
