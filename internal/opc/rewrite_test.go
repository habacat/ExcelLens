package opc

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestRewritePreservesRawPartsAndDetectsCorruptPayload(t *testing.T) {
	var input, output bytes.Buffer
	archive := zip.NewWriter(&input)
	for _, name := range []string{"format.xml", "sheet.xml", "chain.xml"} {
		w, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(w, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	open := func(data []byte) *zip.Reader {
		t.Helper()
		r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	receipt, err := Rewrite(t.Context(), &output, open(input.Bytes()), func(part *zip.File) (Change, error) {
		switch part.Name {
		case "sheet.xml":
			return Change{Write: func(src io.Reader, out io.Writer) error { _, err := io.WriteString(out, "updated"); return err }}, nil
		case "chain.xml":
			return Change{Remove: true}, nil
		default:
			return Change{}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	result := open(output.Bytes())
	if err = receipt.Verify(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Parts) != 2 || receipt.Changed["format.xml"] || !receipt.Changed["sheet.xml"] || !receipt.Changed["chain.xml"] {
		t.Fatal(receipt)
	}
	// The central directory still advertises the original CRC after a stored
	// payload byte is damaged. Verification must read to EOF to see this.
	offset, err := result.File[0].DataOffset()
	if err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(output.Bytes())
	bad[offset] ^= 1
	if err = receipt.Verify(t.Context(), open(bad)); !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("expected CRC failure, got %v", err)
	}
	if err = receipt.Verify(t.Context(), open(input.Bytes())); err == nil {
		t.Fatal("unexpected part accepted")
	}
}
