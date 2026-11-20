package editfile

import (
	"archive/zip"
	"bytes"
	"context"
	"io"

	"excellens/internal/opc"
)

// Cell ownership or values changed. Keep optional shared-string counts and
// the calculation chain from advertising references to the previous revision.
func (p *xlsxPackage) writeValueStructure(ctx context.Context, dst io.Writer, updates map[string][]byte) (opc.Receipt, error) {
	return p.writePatchedValueStructure(ctx, dst, updates, nil)
}

func (p *xlsxPackage) writePatchedValueStructure(ctx context.Context, dst io.Writer, updates map[string][]byte, splices map[string][]xmlPatch) (opc.Receipt, error) {
	for _, part := range []string{"xl/workbook.xml", "xl/sharedStrings.xml", "[Content_Types].xml", "xl/_rels/workbook.xml.rels"} {
		if p.files[part] == nil {
			continue
		}
		raw, ok := updates[part]
		var err error
		if !ok {
			raw, err = p.readPart(ctx, part)
			if err != nil {
				return opc.Receipt{}, err
			}
		}
		var patches []xmlPatch
		switch part {
		case "xl/workbook.xml":
			patches, err = calcPropertiesPatches(ctx, raw)
		case "xl/sharedStrings.xml":
			patches, err = sharedCountPatches(ctx, raw)
		default:
			if p.files[calcChainPart] != nil {
				patches, err = calcChainReferencePatch(ctx, raw, part)
			}
		}
		if err != nil {
			return opc.Receipt{}, err
		}
		next, err := applyXML(ctx, raw, patches)
		if err != nil {
			return opc.Receipt{}, err
		}
		if !bytes.Equal(next, raw) {
			updates[part] = next
		}
	}
	added := map[string][]byte{}
	for name, data := range updates {
		if p.files[name] == nil {
			added[name] = data
		}
	}
	return opc.RewriteWithAdditions(ctx, dst, p.zip, added, func(f *zip.File) (opc.Change, error) {
		if f.Name == calcChainPart {
			return opc.Change{Remove: true}, nil
		}
		next, ok := updates[f.Name]
		if !ok {
			if ps := splices[f.Name]; len(ps) > 0 {
				return opc.Change{Write: func(src io.Reader, out io.Writer) error {
					return writePartPatches(ctx, out, src, int64(f.UncompressedSize64), ps)
				}}, nil
			}
			return opc.Change{}, nil
		}
		if next == nil {
			return opc.Change{Remove: true}, nil
		}
		return opc.Change{Write: func(_ io.Reader, w io.Writer) error { _, err := w.Write(next); return err }}, nil
	})
}
