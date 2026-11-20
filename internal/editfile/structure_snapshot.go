package editfile

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

// MaterializeStructure runs while the document lease is held. A confirmed
// revision never shares a filename with a later branch of its undo history.
func MaterializeStructure(ctx context.Context, dir string, doc *editdoc.Document, layout Layout, src *workbook.EditSourceFile, op editdoc.Structure) (string, error) {
	write := func(prior *xlsxPackage) (string, error) {
		return materializeRevision(ctx, dir, doc, layout, src, doc.Snapshot(), doc.ColumnFormulas(), func(w io.Writer, source *workbook.EditSourceFile) (opc.Receipt, error) {
			return rewriteStructureFrom(ctx, w, source, op, prior)
		})
	}
	if op.Kind != "copyRange" && op.Kind != "cutRange" {
		return write(nil)
	}
	view, ok := doc.TransferSource(op.SourceRevision)
	if !ok || op.Kind == "cutRange" && view != doc {
		return "", errors.New("来源修订已变化，请重新复制或剪切")
	}
	if view == doc {
		return write(nil)
	}
	return withPriorTransferSource(ctx, dir, view, write)
}

func materializeRevision(ctx context.Context, dir string, doc *editdoc.Document, layout Layout, src *workbook.EditSourceFile, values map[editdoc.Address]editdoc.Value, templates map[editdoc.Address]string, rewrite func(io.Writer, *workbook.EditSourceFile) (opc.Receipt, error)) (string, error) {
	if layout.XLSX == nil {
		return "", ErrTarget
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	next := filepath.Join(dir, "r"+strconv.FormatUint(doc.Revision+1, 10)+"."+doc.Base.Source.Format)
	pending := filepath.Join(dir, ".writing")
	current := filepath.Join(dir, ".overlay")
	defer os.Remove(current)
	defer os.Remove(pending)
	if len(values) != 0 || doc.Rules != 0 || len(templates) != 0 {
		if err := materializeOverlay(ctx, current, doc, layout, src, values, templates); err != nil {
			return "", err
		}
		f, err := workbook.OpenEditSource(current)
		if err != nil {
			return "", err
		}
		defer f.Close()
		src = f
	}
	out, err := os.OpenFile(pending, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	write := &boundedCopy{ctx: ctx, file: out}
	receipt, err := rewrite(write, src)
	if err == nil && len(receipt.Changed) == 0 {
		err = editdoc.ErrNoChange
	}
	if err == nil {
		var archive *zip.Reader
		archive, err = zip.NewReader(out, write.bytes)
		if err == nil {
			err = receipt.Verify(ctx, archive)
		}
	}
	if err == nil {
		err = out.Sync()
	}
	if err = errors.Join(err, out.Close()); err != nil {
		return "", err
	}
	if err = src.Check(); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var bytes int64
	for _, e := range entries {
		if e.Name() == filepath.Base(next) || e.Name() == filepath.Base(current) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return "", err
		}
		bytes += info.Size()
	}
	if bytes > formats.EditStructureHistoryBytes {
		return "", fmt.Errorf("工作簿结构历史超过 %d GB 上限", formats.EditStructureHistoryBytes>>30)
	}
	if err = os.Rename(pending, next); err != nil {
		return "", err
	}
	return next, nil
}

func materializeOverlay(ctx context.Context, path string, doc *editdoc.Document, layout Layout, src *workbook.EditSourceFile, values map[editdoc.Address]editdoc.Value, templates map[editdoc.Address]string) error {
	out, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	verify, err := layout.Write(ctx, &boundedCopy{ctx: ctx, file: out}, src, doc.Base.Source.Stamp, values, doc.RuleChanges(), templates, TextOptions{}, &Artifact{})
	if err == nil {
		err = verify(out)
	}
	return errors.Join(err, out.Close())
}
