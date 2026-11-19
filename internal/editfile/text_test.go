package editfile

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func isolateEditFileStorage(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "HOME"} {
		t.Setenv(key, root)
	}
}

func openTextFixture(t *testing.T, path string) (*workbook.EditSourceFile, TextLayout, string) {
	t.Helper()
	w, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.ClearIndexes)
	f, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := w.CheckEditSource(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	l, proof, err := InspectText(t.Context(), f, w)
	if err != nil {
		t.Fatal(err)
	}
	return f, l, proof
}

func TestTextCopyPreservesFieldWidthAndOriginalNewlines(t *testing.T) {
	isolateEditFileStorage(t)
	original := "\r\n字段,值,尾列\r\n编码,00123,\n短行\n\r\n多行,\"甲\r\n乙\r丙\n\"\"丁\"\r\n\"\"\r\n=公式,    空白"
	for _, enc := range []string{"UTF-8", "UTF-8 BOM", "UTF-16 LE", "UTF-16 BE", "GB18030"} {
		t.Run(enc, func(t *testing.T) {
			codec, _, mark, _ := textEncoding(enc)
			data := []byte(original)
			if codec != nil {
				var err error
				data, err = codec.NewEncoder().Bytes(data)
				if err != nil {
					t.Fatal(err)
				}
			}
			data = append(mark, data...)
			path := filepath.Join(t.TempDir(), "billing.csv")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			f, layout, proof := openTextFixture(t, path)
			if layout.Rows != 6 || layout.Columns != 3 || layout.Encoding != enc || layout.MarkBytes != len(mark) || proof != fmt.Sprintf("%x", sha256.Sum256(data)) {
				t.Fatalf("unexpected preflight: %+v, %s", layout, proof)
			}
			a, b, c, d := editdoc.Address{Sheet: layout.Sheet, Row: 2, Col: 2}, editdoc.Address{Sheet: layout.Sheet, Row: 2, Col: 3}, editdoc.Address{Sheet: layout.Sheet, Row: 3, Col: 3}, editdoc.Address{Sheet: layout.Sheet, Row: 4, Col: 2}
			values, err := ReadTextCells(t.Context(), f, layout, []editdoc.Address{d, a, b, c})
			if err != nil || values[a].Raw != "00123" || values[b].Kind != "blank" || values[c].Kind != "missing" || values[d].Raw != "甲\r\n乙\r丙\n\"丁" {
				t.Fatalf("logical cells: %#v %v", values, err)
			}
			for _, options := range []TextOptions{{"source", false}, {"utf8-bom", true}} {
				out, err := os.CreateTemp(t.TempDir(), "copy-*.csv")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = out.Close() })
				edits := map[editdoc.Address]editdoc.Value{a: {Kind: "text", Raw: "000001234567890123456789"}, d: {Kind: "text", Raw: "引号\"和\r\n换行"}}
				result, err := WriteText(t.Context(), out, f, layout, proof, edits, options)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := out.Seek(0, 0); err != nil {
					t.Fatal(err)
				}
				if err := VerifyText(t.Context(), out, result); err != nil {
					t.Fatal(err)
				}
				if err := out.Close(); err != nil {
					t.Fatal(err)
				}
				copyFile, copyLayout, _ := openTextFixture(t, out.Name())
				got, err := ReadTextCells(t.Context(), copyFile, copyLayout, []editdoc.Address{a, b, c, d, {Sheet: layout.Sheet, Row: 6, Col: 1}})
				expected := map[editdoc.Address]editdoc.Value{a: edits[a], b: values[b], c: values[c], d: edits[d], {Sheet: layout.Sheet, Row: 6, Col: 1}: {Kind: "text", Raw: "=公式"}}
				if options.Protect {
					expected[editdoc.Address{Sheet: layout.Sheet, Row: 6, Col: 1}] = editdoc.Value{Kind: "text", Raw: "'=公式"}
				}
				if err != nil || !reflect.DeepEqual(got, expected) || (result.Protected != 0) != options.Protect {
					t.Fatalf("round trip: %#v %v, protection count %d", got, err, result.Protected)
				}
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(data, after) {
				t.Fatal("source bytes changed", err)
			}
		})
	}
}

func TestMalformedTextPreflightRejectsIncompleteRecord(t *testing.T) {
	isolateEditFileStorage(t)
	path := filepath.Join(t.TempDir(), "partial.tsv")
	if err := os.WriteFile(path, []byte("A\tB\n1\t\"broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if l, _, err := InspectText(t.Context(), f, w); err == nil || !strings.Contains(err.Error(), "不完整") || l.Valid() && l.Rows > 1 {
		t.Fatalf("partial record accepted: %+v %v", l, err)
	}
}

func TestSparseTextMillion(t *testing.T) {
	if os.Getenv("EXCELLENS_EDIT_SCALE") != "1" {
		t.Skip("optional million-record edit verification")
	}
	isolateEditFileStorage(t)
	path := filepath.Join(t.TempDir(), "million.csv")
	source, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(source, 128<<10)
	for i := 1; i <= 1_000_000; i++ {
		if _, err := w.WriteString(strconv.Itoa(i) + ",00000001,\r\n"); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f, l, proof := openTextFixture(t, path)
	t.Logf("preflight: rows=%d source=%d bytes offsets=%d elapsed=%s", l.Rows, l.Size, len(l.Offsets), time.Since(start))
	layout, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	journalRoot := t.TempDir()
	store, err := checkpoint.Open(journalRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, count := range []int{1, 100, 10000} {
		start := time.Now()
		var targets []editdoc.Address
		for i := 0; i < count; i++ {
			targets = append(targets, editdoc.Address{Sheet: l.Sheet, Row: 1 + (i+1)*999999/count, Col: 3})
		}
		values, err := ReadTextCells(t.Context(), f, l, targets)
		if err != nil || len(values) != count {
			t.Fatal(len(values), err)
		}
		changes := make([]editdoc.Change, 0, len(values))
		for pos, value := range values {
			if value.Kind != "blank" {
				t.Fatal("wrong source coordinate", pos, value)
			}
			changes = append(changes, editdoc.Change{Address: pos, Value: editdoc.Value{Kind: "text", Raw: "03459"}})
		}
		took := time.Since(start)
		entry, err := editdoc.Create(t.Context(), store, editdoc.Base{Source: checkpoint.Source{Path: path, Book: proof, Fingerprint: proof, Format: "csv", Source: "csv", Stamp: proof}, Name: "million.csv", Layout: layout})
		if err != nil {
			t.Fatal(err)
		}
		load := func() *editdoc.Document {
			t.Helper()
			doc, err := editdoc.Load(t.Context(), store, entry.Key)
			if err != nil {
				t.Fatal(err)
			}
			return doc
		}
		doc := load()
		verifiedBase := func([]editdoc.Change) (map[editdoc.Address]editdoc.Value, error) { return values, nil }
		started := time.Now()
		for i, action := range []string{"change", "undo", "redo"} {
			op := editdoc.Operation{Request: fmt.Sprintf("%032x", i+1), Revision: uint64(i + 1), Action: action}
			if i == 0 {
				op.Changes = changes
			}
			if rev, err := doc.Commit(t.Context(), op, verifiedBase, nil); err != nil || rev != uint64(i+2) {
				t.Fatalf("%s rev=%d: %v", action, rev, err)
			}
		}
		commitTime := time.Since(started)
		if err := doc.Close(); err != nil {
			t.Fatal(err)
		}
		started = time.Now()
		doc = load()
		snapshot := doc.Snapshot()
		if doc.Revision != 4 || len(snapshot) != count {
			t.Fatal(doc.Info)
		}
		if err := doc.Close(); err != nil {
			t.Fatal(err)
		}
		restoreTime := time.Since(started)
		journal, err := os.Stat(filepath.Join(journalRoot, entry.ID, "blocks"))
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.CreateTemp(t.TempDir(), "roundtrip-*.csv")
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		result, err := WriteText(t.Context(), out, f, l, proof, snapshot, TextOptions{"source", false})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := out.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		if err := VerifyText(t.Context(), out, result); err != nil {
			t.Fatal(err)
		}
		_ = out.Close()
		t.Logf("edits=%d sparse-read=%s commit-undo-redo=%s restore=%s journal=%d bytes write-and-verify=%s", count, took, commitTime, restoreTime, journal.Size(), time.Since(start))
	}
}
