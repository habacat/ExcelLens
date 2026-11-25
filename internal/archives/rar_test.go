package archives

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwaples/rardecode/v2"
)

// The tiny fixtures were packed with WinRAR 6.24 (-ma4) and 7.00 (-ma5)
// from synthetic data. A Markdown member checks the shared extension filter.
func TestRARSelectedMember(t *testing.T) {
	isolateArchiveStorage(t)
	for _, tc := range []struct {
		file, member, line string
		repeat, count      int
		solid              bool
	}{
		{"rar4.rar", "01-凭证.csv", "000123,4.50\n", 512, 1, false},
		{"rar5-solid.rar", "02-明细.csv", "000789,6.75\n", 384, 2, true},
	} {
		t.Run(tc.file, func(t *testing.T) {
			source := filepath.Join("testdata", tc.file)
			original, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			listing, cancelListing := context.WithCancel(t.Context())
			a, err := Open(listing, source, "")
			cancelListing()
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if a.Format != "RAR" || a.Total != tc.count+1 || len(a.Members) != tc.count || a.CacheDir() != "" {
				t.Fatalf("unexpected RAR directory or eager extraction: %s %d %+v %q", a.Format, a.Total, a.Members, a.CacheDir())
			}
			id := tc.count - 1
			want := "编号,金额\n" + strings.Repeat(tc.line, tc.repeat)
			if m := a.Members[id]; m.Name != tc.member || m.Size != int64(len(want)) || m.Packed >= uint64(m.Size) {
				t.Fatalf("unexpected compressed member: %+v", m)
			}

			if tc.solid {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				path, _, err := a.Extract(ctx, id, func(_, _ int64, stage string) {
					if strings.Contains(stage, "固实 RAR") {
						cancel()
					}
				})
				if path != "" || !errors.Is(err, context.Canceled) {
					t.Fatalf("solid selection did not cancel: %q %v", path, err)
				}
			}
			path, m, err := a.Extract(t.Context(), id, nil)
			if err != nil || m.Name != tc.member {
				t.Fatal(m, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Fatalf("selected bytes differ: %d bytes, %v", len(got), err)
			}
			entries, err := os.ReadDir(a.CacheDir())
			if err != nil || len(entries) != 2 || filepath.Dir(path) != a.CacheDir() {
				t.Fatalf("cache must contain only the selected member and receipt: %v %v", entries, err)
			}
			reopened, _, err := a.Extract(t.Context(), id, nil)
			if err != nil || reopened != path {
				t.Fatal(reopened, err)
			}
			got, err = os.ReadFile(source)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("source RAR changed", err)
			}
		})
	}
}

func TestRARUnreadableArchives(t *testing.T) {
	isolateArchiveStorage(t)
	for _, tc := range []struct {
		file, message string
		cause         error
	}{
		{"rar5-locked.rar", "RAR 已加密", rardecode.ErrArchiveEncrypted},
		{"rar5-split.part1.rar", "单文件 RAR", rardecode.ErrMultiVolume},
	} {
		t.Run(tc.file, func(t *testing.T) {
			a, err := Open(t.Context(), filepath.Join("testdata", tc.file), "")
			if a != nil || !errors.Is(err, tc.cause) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("unexpected RAR open result: %v, %v", a, err)
			}
		})
	}
}

func TestRARChecksumAndTruncation(t *testing.T) {
	isolateArchiveStorage(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "rar5-stored.rar"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("id,value\n000009,5.25\n")
	start := bytes.Index(raw, data)
	if start < 0 {
		t.Fatal("stored payload absent")
	}

	dir := t.TempDir()
	cut := filepath.Join(dir, "cut.rar")
	if err := os.WriteFile(cut, raw[:start+1], 0600); err != nil {
		t.Fatal(err)
	}
	if a, err := Open(t.Context(), cut, ""); a != nil || err == nil || !strings.Contains(err.Error(), "不完整") {
		t.Fatalf("truncated archive exposed members: %v, %v", a, err)
	}

	raw[start] ^= 1
	damaged := filepath.Join(dir, "damaged.rar")
	if err := os.WriteFile(damaged, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), damaged, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	path, _, err := a.Extract(t.Context(), 0, nil)
	if path != "" || !errors.Is(err, rardecode.ErrBadFileChecksum) {
		t.Fatalf("corrupt archive member unexpectedly published: %q %v", path, err)
	}
	entries, err := os.ReadDir(a.CacheDir())
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache contains a failed extraction: %v %v", entries, err)
	}
}
