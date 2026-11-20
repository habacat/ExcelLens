package editfile

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"

	"excellens/internal/opc"
	"excellens/internal/workbook"
)

// The recalculation graph must be built against the new set of ZIP parts.
// Excel stores a deleted sheet reference as #REF! in a cell formula.
func (p *xlsxPackage) writeSheetDeletion(ctx context.Context, dst io.Writer, updates map[string][]byte, views map[string][]xmlPatch) (opc.Receipt, error) {
	var out opc.Receipt
	write := func(w io.Writer) (opc.Receipt, error) { return p.writePatchedValueStructure(ctx, w, updates, views) }
	err := withWrittenStructure(ctx, "", write, func(next *xlsxPackage, file *workbook.EditSourceFile) error {
		book, err := next.readPart(ctx, workbookPart)
		if err != nil {
			return err
		}
		out, err = next.finishCellStructure(ctx, dst, file, map[string][]byte{workbookPart: book}, nil)
		return err
	})
	return out, err
}

// Validate the written package before another operation binds styles, sheet IDs
// or rules from it. The caller publishes only the final transaction.
func (p *xlsxPackage) withRevisedFile(ctx context.Context, dir string, updates map[string][]byte, run func(*xlsxPackage, *workbook.EditSourceFile) error) error {
	return withWrittenStructure(ctx, dir, func(w io.Writer) (opc.Receipt, error) { return p.writeValueStructure(ctx, w, updates) }, run)
}

func withWrittenStructure(ctx context.Context, dir string, write func(io.Writer) (opc.Receipt, error), run func(*xlsxPackage, *workbook.EditSourceFile) error) error {
	f, err := os.CreateTemp(dir, ".excellens-edit-*.xlsx")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	out := &boundedCopy{ctx: ctx, file: f}
	receipt, err := write(out)
	if err == nil {
		var z *zip.Reader
		z, err = zip.NewReader(f, out.bytes)
		if err == nil {
			err = receipt.Verify(ctx, z)
		}
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	file, err := workbook.OpenEditSource(f.Name())
	if err != nil {
		return err
	}
	defer file.Close()
	next, err := openStructurePackage(ctx, file)
	if err != nil {
		return err
	}
	return run(next, file)
}
