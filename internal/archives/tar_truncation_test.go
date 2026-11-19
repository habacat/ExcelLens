package archives

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func truncatedTarFixture(t *testing.T, priorName, name string, fragment []byte, declared int64) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if priorName != "" {
		if err := w.WriteHeader(&tar.Header{Name: priorName, Mode: 0600, Size: 4}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("A\n1\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: declared}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(fragment); err != nil {
		t.Fatal(err)
	}
	// Deliberately omit the rest of the body and TAR trailer.
	return b.Bytes()
}

func TestTruncatedTARTextNeedsExplicitRecoveryAndKeepsRawBytes(t *testing.T) {
	isolateArchiveStorage(t)
	fragment := []byte("完整原文\n末条被切断")
	data := truncatedTarFixture(t, "first.csv", "last.txt", fragment, 1000)
	path := filepath.Join(t.TempDir(), "cut.tar")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.Members) != 2 || a.Total != 2 || !a.Members[1].Incomplete || a.Members[1].DeclaredSize != 1000 || a.Members[1].Size != int64(len(fragment)) || !strings.Contains(a.Notice, "不完整") {
		t.Fatalf("directory %+v / %s", a.Members, a.Notice)
	}
	if a.CacheDir() != "" {
		t.Fatal("listing extracted data")
	}
	if _, _, err := a.Extract(context.Background(), 1, nil); err == nil {
		t.Fatal("partial member opened as complete")
	}
	full, _, err := a.Extract(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(full); string(got) != "A\n1\n" {
		t.Fatal("complete member changed")
	}
	out, _, err := a.ExtractFragment(context.Background(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, fragment) {
		t.Fatal("raw fragment was modified")
	}
	if _, _, err := a.Extract(context.Background(), 1, nil); err == nil {
		t.Fatal("fragment cache bypassed normal extraction guard")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
		t.Fatal("source TAR changed")
	}
}

func TestTruncatedTARBinaryHeadersAndWAL(t *testing.T) {
	isolateArchiveStorage(t)
	for _, ext := range []string{".xlsx", ".xls", ".sqlite", ".zip", ".csv", ".tsv"} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cut.tar")
			if err := os.WriteFile(path, truncatedTarFixture(t, "", "cut"+ext, []byte("short"), 1024), 0600); err != nil {
				t.Fatal(err)
			}
			if a, err := Open(context.Background(), path, ""); err == nil {
				a.Close()
				t.Fatal("unsafe binary/ambiguous record recovery")
			} else if !strings.Contains(err.Error(), "声明 1024 字节") || !strings.Contains(err.Error(), "实际仅存 5 字节") {
				t.Fatal(err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "wal.tar")
	if err := os.WriteFile(path, truncatedTarFixture(t, "ledger.db", "ledger.db-wal", []byte("short"), 1024), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, _, err := a.Extract(context.Background(), 0, nil); err == nil || !strings.Contains(err.Error(), "WAL 日志不完整") {
		t.Fatal("truncated WAL ignored", err)
	}
	data := truncatedTarFixture(t, "first.csv", "second.txt", []byte("short"), 1024)
	for _, cutHeader := range []bool{false, true} {
		copyData := append([]byte(nil), data...)
		if cutHeader {
			copyData = copyData[:1024+77]
		} else {
			copyData[1024+148] ^= 1
		}
		path := filepath.Join(t.TempDir(), "header.tar")
		if err := os.WriteFile(path, copyData, 0600); err != nil {
			t.Fatal(err)
		}
		a, err := Open(context.Background(), path, "")
		if cutHeader {
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Members) != 1 || a.Notice == "" {
				t.Fatal("missing header not marked", a.Notice)
			}
			a.Close()
		} else if err == nil {
			a.Close()
			t.Fatal("corrupt checksum was treated as truncation")
		}
	}
}

func TestTruncatedTARInsideCompleteGZIP(t *testing.T) {
	isolateArchiveStorage(t)
	fragment := []byte("first line\ncut off")
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(truncatedTarFixture(t, "", "fragment.txt", fragment, 1234)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cut.tar.gz")
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Format != "TAR.GZ" || a.Notice == "" || len(a.Members) != 1 || !a.Members[0].Incomplete {
		t.Fatal("container lost truncation metadata", a.Notice)
	}
	out, _, err := a.ExtractFragment(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, fragment) {
		t.Fatal("wrong container offset")
	}
}

func TestProvidedTruncatedTAR(t *testing.T) {
	path := os.Getenv("EXCELLENS_REAL_ARCHIVETAR")
	if path == "" {
		t.Skip("requires supplied truncated TAR")
	}
	isolateArchiveStorage(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil || fmt.Sprintf("%x", h.Sum(nil)) != "f2ae7df8fb90946e3e319dbcbed3a334aead7c193063cf0242bf3c8944265ef5" {
		t.Fatal("different source fixture", err)
	}
	started := time.Now()
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	t.Logf("directory listing: %s", time.Since(started))
	if len(a.Members) != 1 || !a.Members[0].Incomplete || a.Members[0].DeclaredSize != 701024150 || a.Members[0].Size != 114294272 {
		t.Fatalf("unexpected directory %+v", a.Members)
	}
	out, _, err := a.ExtractFragment(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	rawHash := sha256.New()
	_, err = io.Copy(rawHash, raw)
	raw.Close()
	if err != nil || fmt.Sprintf("%x", rawHash.Sum(nil)) != "cc82e4d2429d46b8b076fe8ea36bbe838550e66e45c4e321459e0e0d15751962" {
		t.Fatal("raw fragment bytes changed", err)
	}
	wb, err := workbook.OpenTextFragment(out)
	if err != nil {
		t.Fatal(err)
	}
	if !wb.SourceInfo.Incomplete || wb.SourceInfo.TableHeader == nil || len(wb.SourceInfo.TableHeader.Columns) != 39 {
		t.Fatalf("source %+v", wb.SourceInfo)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.RowCount() != 345030 || !idx.RowsMapped() || idx.MaxRow != 345030 || idx.MaxCol != 39 || !strings.Contains(wb.SourceInfo.Notice, "67 字节") {
		t.Fatalf("recovered shape/tail differs: %d x %d / %s", idx.MaxRow, idx.MaxCol, wb.SourceInfo.Notice)
	}
	prefix := idx.FirstRows(4000)
	if prefix.RowCount() != 4000 || prefix.RowAt(3999).Number != 4000 || prefix.FindRowOffset(4001) != prefix.RowCount() {
		t.Fatal("real search sample did not stop at its 4000th indexed row")
	}
	t.Logf("recovery + index: %s; %d x %d; %s", time.Since(started), idx.MaxRow, idx.MaxCol, wb.SourceInfo.Notice)
	// Every field, empty position and original row coordinate is checked
	// against an independently computed Python digest below.
	digest := sha256.New()
	for start := 0; start < idx.RowCount(); start += 4096 {
		rows, err := wb.ReadRows(idx, start, min(4096, idx.RowCount()-start), 1, 39)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != min(4096, idx.RowCount()-start) {
			t.Fatal("row range was shortened")
		}
		for j, row := range rows {
			if row.Number != start+j+1 {
				t.Fatal("row coordinate changed")
			}
			fields := make([]string, 39)
			for _, cell := range row.Cells {
				fields[cell.Col-1] = cell.Raw
			}
			fmt.Fprintln(digest, strings.Join(fields, "@"))
		}
	}
	t.Logf("canonical SHA-256: %x", digest.Sum(nil))
	if fmt.Sprintf("%x", digest.Sum(nil)) != "54e5a7edd2df0d44e908fe5be536ef0789ae48102cbdd8446224d168152501eb" {
		t.Fatal("recovered fields differ from independent oracle")
	}
}
