package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func TestCSVArchiveStandardReaderRetainsEveryCSVByteAndSafeUnicodeName(t *testing.T) {
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte("编号,金额,状态\r\n001,9007199254740993.31,\"含引号\"\"及换行\n😀\"\r\n"+strings.Repeat("原文,,\"\"\r\n", 10000))...)
	var dst bytes.Buffer
	if err := WriteCSVArchive(context.Background(), &dst, `../来源\完整:关联.csv`, func(out io.Writer) error {
		_, err := io.Copy(out, bytes.NewReader(data))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(dst.Bytes()), int64(dst.Len()))
	if err != nil || len(zr.File) != 1 {
		t.Fatal(err)
	}
	item := zr.File[0]
	if strings.ContainsAny(item.Name, "/\\:") || !strings.Contains(item.Name, "来源") || !strings.HasSuffix(item.Name, ".csv") || item.Method != zip.Deflate || item.UncompressedSize64 != uint64(len(data)) {
		t.Fatal("unsafe name or nonstandard archive", item.Name, item.Method, item.UncompressedSize64)
	}
	f, err := item.Open()
	if err != nil {
		t.Fatal(err)
	}
	reopened, readErr := io.ReadAll(f) // archive/zip verifies CRC on EOF.
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(data, reopened) {
		t.Fatal("CSV bytes or ZIP CRC changed", readErr, closeErr)
	}
}

type csvArchiveFailWriter struct{ remaining int }

func (w *csvArchiveFailWriter) Write(p []byte) (int, error) {
	n := min(len(p), w.remaining)
	w.remaining -= n
	if n < len(p) {
		return n, io.ErrClosedPipe
	}
	return n, nil
}

func TestCSVArchivePropagatesProductionBodyCloseFailureAndCancellation(t *testing.T) {
	produce := func(w io.Writer) error {
		_, err := io.WriteString(w, strings.Repeat("CSV,数据\r\n", 1000))
		return err
	}
	var good bytes.Buffer
	if err := WriteCSVArchive(context.Background(), &good, "result.csv", produce); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, 30, good.Len() - 1} {
		if err := WriteCSVArchive(context.Background(), &csvArchiveFailWriter{limit}, "result.csv", produce); err == nil {
			t.Fatal("short body/central directory published as complete", limit)
		}
	}
	bodyError := errors.New("source record failed")
	if err := WriteCSVArchive(context.Background(), io.Discard, "result.csv", func(io.Writer) error { return bodyError }); !errors.Is(err, bodyError) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := WriteCSVArchive(ctx, io.Discard, "result.csv", func(w io.Writer) error { cancel(); _, err := io.WriteString(w, "remaining"); return err }); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation did not stop producer or ZIP close", err)
	}
	if err := WriteCSVArchive(ctx, io.Discard, "result.csv", func(io.Writer) error { t.Fatal("cancelled archive started producer"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type csvArchiveCancelWriter struct {
	cancel context.CancelFunc
	writes int
}

func (w *csvArchiveCancelWriter) Write(p []byte) (int, error) {
	w.writes++
	w.cancel()
	return len(p), nil
}

func TestCSVArchiveChecksCancellationBetweenBoundedInputChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dst := &csvArchiveCancelWriter{cancel: cancel}
	w := archiveContextWriter{ctx, dst}
	n, err := w.Write(make([]byte, formats.ExportArchiveChunkBytes*3))
	if n != formats.ExportArchiveChunkBytes || !errors.Is(err, context.Canceled) || dst.writes != 1 {
		t.Fatal("large single write ignored cancellation", n, err, dst.writes)
	}
}

func TestCSVArchiveActualZIP64StreamWithoutUncompressedIntermediate(t *testing.T) {
	if os.Getenv("EXCELLENS_CSV_ZIP64") != "1" {
		t.Skip("optional actual 4GiB+1 standard ZIP64 streaming check")
	}
	file := filepath.Join(t.TempDir(), "large.zip")
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	const logicalBytes int64 = 1<<32 + 1
	block := bytes.Repeat([]byte{'a'}, formats.ExportArchiveChunkBytes)
	block[len(block)-1] = '\n'
	want := sha256.New()
	err = WriteCSVArchive(context.Background(), out, "large.csv", func(dst io.Writer) error {
		writer := io.MultiWriter(dst, want)
		for remaining := logicalBytes; remaining > 0; {
			chunk := block[:min(int64(len(block)), remaining)]
			if _, err := writer.Write(chunk); err != nil {
				return err
			}
			remaining -= int64(len(chunk))
		}
		return nil
	})
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(file)
	if err != nil || len(archive.File) != 1 || archive.File[0].UncompressedSize64 != uint64(logicalBytes) {
		t.Fatal("ZIP64 length was truncated", err)
	}
	defer archive.Close()
	member, err := archive.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	got := sha256.New()
	n, err := io.Copy(got, member)
	_ = member.Close()
	if err != nil || n != logicalBytes || !bytes.Equal(want.Sum(nil), got.Sum(nil)) {
		t.Fatal("ZIP64 stream or CRC differs", n, err)
	}
	if receipt := os.Getenv("EXCELLENS_CSV_ZIP64_RECEIPT"); receipt != "" {
		info, _ := os.Stat(file)
		data, _ := json.MarshalIndent(map[string]any{"result": "PASS", "logicalBytes": logicalBytes, "archiveBytes": info.Size(), "sha256": fmt.Sprintf("%x", got.Sum(nil)), "actualBytesWrittenAndDecompressed": true, "standardZIP64LengthAndCRCVerified": true, "uncompressedIntermediateFiles": 0, "sourceRecordsBenchmark": false}, "", "  ")
		if err := os.WriteFile(receipt, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
