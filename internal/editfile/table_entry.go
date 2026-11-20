package editfile

import (
	"context"
	"io"
	"maps"
	"os"
	"path/filepath"

	"excellens/internal/editdoc"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

// The intermediate range is private to the document lease. Validate the
// entire paste against its NEW table and validation rules before publishing
// either the new geometry or the user's data.
type tableEntry struct {
	dir     string
	changes []editdoc.Change
	infer   bool
	anchor  *editdoc.Address
}

func (g *TableGrowth) Materialize(ctx context.Context, dir string, doc *editdoc.Document, layout Layout, src *workbook.EditSourceFile, changes []editdoc.Change, infer bool, anchor *editdoc.Address) (string, error) {
	input := tableEntry{dir: dir, infer: infer, changes: make([]editdoc.Change, len(changes))}
	if anchor != nil {
		a := *anchor
		a.Row += g.rowOffset
		input.anchor = &a
	}
	for i, c := range changes {
		c.Row += g.rowOffset
		if g.rowOffset != 0 && c.Value.Kind == "formula" {
			raw, err := copyRangeFormula(c.Value.Raw, g.rowOffset, 0)
			if err != nil {
				return "", err
			}
			c.Value.Raw = raw
		}
		input.changes[i] = c
	}
	op := editdoc.Structure{Kind: "resizeTable", Sheet: g.sheet, Name: g.table, Range: pivotRectRef(g.bounds)}
	return materializeRevision(ctx, dir, doc, layout, src, doc.Snapshot(), doc.ColumnFormulas(), func(w io.Writer, source *workbook.EditSourceFile) (opc.Receipt, error) {
		p, err := openStructurePackage(ctx, source)
		if err != nil {
			return opc.Receipt{}, err
		}
		return p.resizeTable(ctx, w, source, op, &input)
	})
}

func (entry tableEntry) apply(ctx context.Context, p *xlsxPackage, updates map[string][]byte, roots map[editdoc.Address]editdoc.Value) error {
	path := filepath.Join(entry.dir, ".table-entry")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_TRUNC|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	_, err = p.writeValueStructure(ctx, &boundedCopy{ctx: ctx, file: file}, maps.Clone(updates))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	src, err := workbook.OpenEditSource(path)
	if err != nil {
		return err
	}
	defer src.Close()
	q, err := openStructurePackage(ctx, src)
	if err != nil {
		return err
	}
	layout := Layout{XLSX: &q.layout}
	plan, err := layout.prepareChanges(ctx, src, entry.changes, &editdoc.Document{}, entry.infer, entry.anchor)
	if err != nil {
		return err
	}
	edits := map[editdoc.Address]editdoc.Value{}
	var addresses []editdoc.Address
	for _, c := range plan.Changes {
		edits[c.Address] = c.Value
		addresses = append(addresses, c.Address)
		roots[c.Address] = editdoc.Value{}
	}
	if len(addresses) == 0 {
		return nil
	}
	templates := map[editdoc.Address]string{}
	for _, c := range plan.Templates {
		templates[c.Address] = c.Formula
	}
	patches, err := q.tablePatches(ctx, edits, templates, &XLSXResult{Layout: q.layout})
	if err != nil {
		return err
	}
	edits, styles, err := q.materializeNumberStyles(ctx, edits)
	if err != nil {
		return err
	}
	added, err := q.installEntryStyles(ctx, styles, patches)
	if err != nil {
		return err
	}
	maps.Copy(updates, added)
	for _, s := range q.layout.Sheets {
		if s.Name != addresses[0].Sheet {
			continue
		}
		data, err := q.readPart(ctx, s.Part)
		if err != nil {
			return err
		}
		targets, err := newSheetTargets(s, addresses)
		if err != nil {
			return err
		}
		if _, err = q.scanSheet(ctx, data, s, targets, nil, nil, nil); err != nil {
			return err
		}
		ps, _, _, err := q.worksheetPatches(ctx, data, targets, edits, &s, validateXLSXNumber)
		if err != nil {
			return err
		}
		patches[s.Part] = ps
	}
	for part, ps := range patches {
		data, err := q.readPart(ctx, part)
		if err != nil {
			return err
		}
		updates[part], err = applyXML(ctx, data, ps)
		if err != nil {
			return err
		}
	}
	return src.Check()
}
