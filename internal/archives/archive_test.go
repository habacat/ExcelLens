package archives

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/workbook"
	"github.com/ulikunitz/xz"
)

func isolateArchiveStorage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"LOCALAPPDATA", "XDG_CACHE_HOME"} {
		t.Setenv(key, root)
	}
	return root
}

func TestZIPLazySelectionCacheAndSafePaths(t *testing.T) {
	isolateArchiveStorage(t)
	path := filepath.Join(t.TempDir(), "many.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for _, name := range []string{"data/ledger.txt", "../../outside.csv", "ignored.bin", "data/nested.zip"} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte("账户@金额\n0001@123.450001\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.Members) != 3 || a.Total != 4 || !a.Members[2].Archive {
		t.Fatalf("directory: %+v", a.Members)
	}
	if _, err := os.Stat(workbook.CacheRoot()); !os.IsNotExist(err) {
		t.Fatal("listing eagerly extracted files", err)
	}
	for _, id := range []int{0, 1} {
		out, m, err := a.Extract(context.Background(), id, nil)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(a.CacheDir(), out)
		if err != nil || strings.HasPrefix(rel, "..") || strings.Contains(rel, "outside") {
			t.Fatalf("unsafe output %s: %v", out, err)
		}
		content, err := os.ReadFile(out)
		if err != nil || string(content) != "账户@金额\n0001@123.450001\n" {
			t.Fatal(m, err)
		}
		before, _ := os.Stat(out)
		reused := false
		again, _, err := a.Extract(context.Background(), id, func(_, _ int64, stage string) { reused = reused || strings.Contains(stage, "复用") })
		after, _ := os.Stat(out)
		if err != nil || again != out || !reused || !before.ModTime().Equal(after.ModTime()) {
			t.Fatal("cache not reused", err)
		}
	}
	entries, err := os.ReadDir(a.CacheDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("unselected files were extracted: %d files", len(entries))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := a.Extract(ctx, 2, nil); err == nil {
		t.Fatal("cancelled extraction proceeded")
	}
}

func tarFixture(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := tar.NewWriter(&buffer)
	for _, entry := range []struct{ name, value string }{{"first.txt", "A@金额\n0001@1.23\n"}, {"padding.bin", strings.Repeat("skip", 10000)}, {"中文/last.csv", "账号,金额\n00002,987.654321\n"}} {
		if err := w.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.value))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(entry.value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestTARAndCompressedContainers(t *testing.T) {
	isolateArchiveStorage(t)
	data := tarFixture(t)
	for _, ext := range []string{".tar", ".tar.gz", ".tgz", ".tar.xz", ".txz"} {
		t.Run(ext, func(t *testing.T) {
			var encoded bytes.Buffer
			switch ext {
			case ".tar":
				encoded.Write(data)
			case ".tar.gz", ".tgz":
				w := gzip.NewWriter(&encoded)
				_, _ = w.Write(data)
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
			default:
				w, err := xz.NewWriter(&encoded)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write(data)
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(t.TempDir(), "ledger"+ext)
			if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			a, err := Open(context.Background(), path, "")
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if len(a.Members) != 2 {
				t.Fatalf("members %+v", a.Members)
			}
			out, m, err := a.Extract(context.Background(), 1, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(out)
			if err != nil || string(got) != "账号,金额\n00002,987.654321\n" || m.Name != "中文/last.csv" {
				t.Fatalf("wrong TAR offset: %q %+v %v", got, m, err)
			}
		})
	}
}

func TestSingleGZIPAndCorruption(t *testing.T) {
	isolateArchiveStorage(t)
	var buffer bytes.Buffer
	w := gzip.NewWriter(&buffer)
	w.Name = "folder/ledger.txt"
	_, _ = w.Write([]byte("编号@借方@贷方\n0001@7.28@\n"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.gz")
	if err := os.WriteFile(path, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	out, m, err := a.Extract(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "ledger.txt" && m.Name != "folder/ledger.txt" {
		t.Fatal(m.Name)
	}
	wb, err := workbook.OpenWorkbook(out)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil || idx.MaxCol != 3 || idx.MaxRow != 2 {
		t.Fatal(idx, err)
	}
	if err := os.WriteFile(out, []byte("damaged cache"), 0600); err != nil {
		t.Fatal(err)
	}
	restored, _, err := a.Extract(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(restored)
	if !bytes.Contains(content, []byte("7.28")) {
		t.Fatal("corrupted cache reused")
	}
}

func TestExtractionFailureIsAtomic(t *testing.T) {
	isolateArchiveStorage(t)
	path := filepath.Join(t.TempDir(), "source.gz")
	var buffer bytes.Buffer
	w := gzip.NewWriter(&buffer)
	_, _ = w.Write([]byte("ID,amount\n1,2\n"))
	_ = w.Close()
	_ = os.WriteFile(path, buffer.Bytes(), 0600)
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, tc := range []struct {
		key             string
		limit, expected int64
		crc             uint32
	}{{"limit.txt", 4, -1, 0}, {"short.txt", 100, 99, 0}, {"crc.txt", 100, -1, 1}} {
		if _, err := a.cacheStream(context.Background(), tc.key, strings.NewReader("1234567890"), tc.limit, tc.expected, tc.crc, nil); err == nil {
			t.Fatal("invalid extraction published", tc.key)
		}
	}
	entries, err := os.ReadDir(a.CacheDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("partial extraction survived: %+v", entries)
	}
	if _, err := boundedLZMA2([]byte{40}, 0, []io.ReadCloser{io.NopCloser(strings.NewReader(""))}); err == nil {
		t.Fatal("unbounded dictionary accepted")
	}
}

func Test7ZIPAndBZ2Fixtures(t *testing.T) {
	isolateArchiveStorage(t)
	for _, file := range []string{"ledger.7z", "ledger.tar.bz2", "ledger.txt.bz2"} {
		a, err := Open(context.Background(), filepath.Join("testdata", file), "")
		if err != nil {
			t.Fatal(file, err)
		}
		if len(a.Members) == 0 {
			t.Fatal("missing fixture members")
		}
		for _, m := range a.Members {
			out, _, err := a.Extract(context.Background(), m.ID, nil)
			if err != nil {
				t.Fatal(file, err)
			}
			b, err := os.ReadFile(out)
			if err != nil || !bytes.Contains(b, []byte("000123")) {
				t.Fatalf("%s wrong bytes %q: %v", file, b, err)
			}
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProvided7ZIPArchive(t *testing.T) {
	path := os.Getenv("EXCELLENS_REAL_ARCHIVE7Z")
	if path == "" {
		t.Skip("requires supplied 7z fixture")
	}
	isolateArchiveStorage(t)
	started := time.Now()
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	t.Logf("7z directory listing: %s", time.Since(started))
	if len(a.Members) != 1 || a.Members[0].Size != 1073465131 {
		t.Fatal(a.Members)
	}
	out, _, err := a.Extract(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != "796fc065e39609bbbd6574cc6c1860bc15dc677547f27827e4fdb09aea7e58f6" {
		t.Fatal("7z extraction digest", got)
	}
	t.Logf("7z extraction and independent full SHA-256: %s", time.Since(started))
	started = time.Now()
	reused := false
	_, _, err = a.Extract(context.Background(), 0, func(_, _ int64, stage string) { reused = reused || strings.Contains(stage, "复用") })
	if err != nil || !reused {
		t.Fatal("cache hit failed", err)
	}
	t.Logf("repeat member open (no decode): %s", time.Since(started))
}

func Test7ZIPCancellationCoversCompressedReaderAndSolidPositioning(t *testing.T) {
	isolateArchiveStorage(t)
	a, err := Open(context.Background(), filepath.Join("testdata", "ledger.7z"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err = a.Extract(ctx, 1, func(_, _ int64, stage string) {
		if strings.Contains(stage, "定位") {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("solid positioning did not honor cancellation", err)
	}
	var b [1]byte
	if n, err := (contextReaderAt{ctx, bytes.NewReader([]byte("x"))}).ReadAt(b[:], 0); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled compressed read proceeded", n, err)
	}
}
