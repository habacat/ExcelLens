package editfile

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestPublicationKeepsPreviousReceiptOnFailedOrCanceledCopy(t *testing.T) {
	o, err := OpenOutputs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	doc := strings.Repeat("a", 32)
	first := Artifact{Key: "1-source-exact", Revision: 1, Format: "csv", Name: "原表-副本-r1.csv"}
	second := Artifact{Key: "2-source-exact", Revision: 2, Format: "csv", Name: "原表-副本-r2.csv"}
	produce := func(dst io.Writer, _ *Artifact) (func(*os.File) error, error) {
		_, err := io.WriteString(dst, "id,value\r\n1,unchanged\r\n")
		return func(*os.File) error { return nil }, err
	}
	ready, err := o.Publish(t.Context(), doc, first, produce, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	fail := errors.New("verification failure")
	_, err = o.Publish(t.Context(), doc, second, func(dst io.Writer, receipt *Artifact) (func(*os.File) error, error) {
		_, err := produce(dst, receipt)
		return func(*os.File) error { return fail }, err
	}, func() error { return nil })
	if !errors.Is(err, fail) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err = o.Publish(ctx, doc, second, produce, func() error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if items, err := o.List(doc); err != nil || len(items) != 1 || items[0] != ready {
		t.Fatal(items, err)
	}
	src, got, err := o.Open(t.Context(), doc, first.Key)
	if err != nil || got != ready {
		t.Fatal(got, err)
	}
	_ = src.Close()
	if _, err := o.Publish(t.Context(), doc, second, produce, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Publish(t.Context(), doc, first, produce, func() error { return nil }); !errors.Is(err, ErrArtifact) {
		t.Fatal("replaced a verified artifact", err)
	}
}
