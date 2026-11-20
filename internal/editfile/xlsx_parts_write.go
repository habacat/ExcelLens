package editfile

import (
	"archive/zip"
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
	receipt, err := opc.RewriteWithAdditions(ctx, dst, p.zip, result.addedParts, func(part *zip.File) (opc.Change, error) {
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
	pos := 0
	for _, p := range patches {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.from < pos || p.to < p.from || p.to > len(source) {
			return ErrXLSX
		}
		if p.from != pos {
			if _, err := dst.Write(source[pos:p.from]); err != nil {
				return err
			}
		}
		if p.data != "" {
			if _, err := io.WriteString(dst, p.data); err != nil {
				return err
			}
		}
		pos = p.to
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := dst.Write(source[pos:])
	return err
}

func writePartPatches(ctx context.Context, out io.Writer, src io.Reader, size int64, ps []xmlPatch) error {
	pos := 0
	// A recalculated sheet can have thousands of splices. Reuse the copy
	// buffer and limiter across them instead of allocating for each XML span.
	limited := io.LimitedReader{R: src}
	buf := make([]byte, 32<<10)
	copySpan := func(dst io.Writer, size int) error {
		if size == 0 {
			return nil
		}
		limited.N = int64(size)
		_, err := io.CopyBuffer(dst, &limited, buf)
		if err == nil && limited.N != 0 {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	for _, patch := range ps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if patch.from < pos || patch.to < patch.from || int64(patch.to) > size {
			return ErrXLSX
		}
		if err := copySpan(out, patch.from-pos); err != nil {
			return err
		}
		if err := copySpan(io.Discard, patch.to-patch.from); err != nil {
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
