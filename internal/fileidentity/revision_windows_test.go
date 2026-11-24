//go:build windows

package fileidentity

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceFingerprintMemoDetectsRestoredTimestampAndReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	first := strings.Repeat("1", 1024)
	if err := os.WriteFile(path, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	file, stable, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := file.Stat()
	stamp, supported := Revision(file, info)
	if !supported {
		_, revisionErr := fingerprintFileRevision(file)
		t.Log("file revision query:", revisionErr)
	}
	file.Close()
	if !stable || !supported {
		t.Log("filesystem lacks a file revision stamp; checking full-hash fallback")
	}
	one, _, err := Fingerprint(path, "txt:test")
	if err != nil {
		t.Fatal(err)
	}
	if stable && supported {
		if got, ok := Recall(stamp, "txt:test"); !ok || got != one {
			t.Fatal("verified source not memoized")
		}
	}
	if again, _, err := Fingerprint(path, "txt:test"); err != nil || again != one {
		t.Fatal("unchanged source changed digest", err)
	}
	// Restoring mtime and preserving size must not fool the memo. ChangeTime
	// is not assumed unique: a file revision must also invalidate the memo.
	second := strings.Repeat("2", len(first))
	if err := os.WriteFile(path, []byte(second), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	two, _, err := Fingerprint(path, "txt:test")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("ExcelLens-external-source-v2:\x00txt:test\x00"+second)))
	if one == two || two != want {
		probe, _, openErr := Open(path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		currentInfo, statErr := probe.Stat()
		if statErr != nil {
			probe.Close()
			t.Fatal(statErr)
		}
		after, _ := Revision(probe, currentInfo)
		probe.Close()
		t.Fatalf("restored mtime reused stale digest; before=%+v after=%+v got=%s want=%s", stamp, after, two, want)
	}
	// A replacement with the same visible metadata has a different identity.
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	three, _, err := Fingerprint(path, "txt:test")
	if err != nil || three != one {
		t.Fatal("replacement used old source identity", err)
	}
}

func TestFingerprintUSNRequiresSupportedCompletePositiveRevision(t *testing.T) {
	for _, version := range []uint16{2, 3} {
		record := make([]byte, 80)
		binary.LittleEndian.PutUint32(record, uint32(len(record)))
		binary.LittleEndian.PutUint16(record[4:], version)
		offset := 24
		if version == 3 {
			offset = 40
		}
		binary.LittleEndian.PutUint64(record[offset:], 12345)
		if usn, ok := fingerprintUSN(record); !ok || usn != 12345 {
			t.Fatal("valid revision rejected", version, usn, ok)
		}
		for i := 0; i < len(record); i++ {
			if _, ok := fingerprintUSN(record[:i]); ok {
				t.Fatal("truncated revision accepted", version, i)
			}
		}
		binary.LittleEndian.PutUint64(record[offset:], 0)
		if _, ok := fingerprintUSN(record); ok {
			t.Fatal("zero journal revision accepted")
		}
		binary.LittleEndian.PutUint16(record[4:], 4)
		if _, ok := fingerprintUSN(record); ok {
			t.Fatal("unknown revision format accepted")
		}
	}
}

func TestSourceFingerprintMemoNeverTrustsAnOpenWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writing.txt")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _, err := Fingerprint(path, "txt:test-writer")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.WriteAt([]byte("after!"), 0); err != nil {
		t.Fatal(err)
	}
	probe, stable, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	probe.Close()
	if stable {
		t.Fatal("memo allowed an existing writer")
	}
	after, _, err := Fingerprint(path, "txt:test-writer")
	if err != nil || after == before {
		t.Fatal("writer bypassed full hashing", err)
	}
}
