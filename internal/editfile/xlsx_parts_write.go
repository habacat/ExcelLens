package editfile

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"io"
	"slices"

	"excellens/internal/opc"
	"excellens/internal/workbook"
)

// Both cell-edit strategies produce offsets into original worksheet parts.
// The package layer owns byte preservation and integrity for either strategy.
func (p *xlsxPackage) writePackage(ctx context.Context, dst io.Writer, source *workbook.EditSourceFile, proof string, result XLSXResult, plan func(string) ([]xmlPatch, error)) (XLSXResult, error) {
	receipt, err := opc.Rewrite(ctx, dst, p.zip, func(part *zip.File) (opc.Change, error) {
		if result.removedCalcChain && part.Name == calcChainPart {
			return opc.Change{Remove: true}, nil
		}
		patches, err := plan(part.Name)
		if err != nil || len(patches) == 0 {
			return opc.Change{}, err
		}
		slices.SortStableFunc(patches, func(a, b xmlPatch) int { return cmp.Compare(a.from, b.from) })
		return opc.Change{Write: func(source io.Reader, out io.Writer) error {
			return writePartPatches(ctx, out, source, int64(part.UncompressedSize64), patches)
		}}, nil
	})
	if err != nil {
		return result, err
	}
	result.Receipt = receipt
	return result, VerifySource(ctx, source, proof)
}

// XML offsets always refer to the uncompressed source bytes. Ordered splices
// apply equally to a retained VML fragment and a streamed worksheet.
func writeXMLPatches(ctx context.Context, dst io.Writer, source []byte, patches []xmlPatch) error {
	return writePartPatches(ctx, dst, bytes.NewReader(source), int64(len(source)), patches)
}

func writePartPatches(ctx context.Context, out io.Writer, src io.Reader, size int64, ps []xmlPatch) error {
	pos := 0
	for _, patch := range ps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if patch.from < pos || patch.to < patch.from || int64(patch.to) > size {
			return ErrXLSX
		}
		if _, err := io.CopyN(out, src, int64(patch.from-pos)); err != nil {
			return err
		}
		if _, err := io.CopyN(io.Discard, src, int64(patch.to-patch.from)); err != nil {
			return err
		}
		if _, err := io.WriteString(out, patch.data); err != nil {
			return err
		}
		pos = patch.to
	}
	_, err := io.Copy(out, src)
	return err
}
