package editdoc

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"maps"
	"slices"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
)

const retainedUndo = 128

type savedReceipt struct {
	Digest   string
	Revision uint64
}
type savedHistory struct {
	Revision   uint64
	Base       Base
	Structures int
	Values     []Change
	Templates  []ColumnFormula
	Rules      []Rule
	Undo, Redo []transaction
	Receipts   map[string]savedReceipt
}

// Compact retains the edited state preceding the recent undo window, followed
// by its reversible transactions. The document identity and revision survive.
func (d *Document) Compact(ctx context.Context, keep int) error {
	keep = min(retainedUndo, max(1, keep))
	u := d.undo[max(0, len(d.undo)-keep):]
	redo := d.redo[max(0, len(d.redo)-keep):]
	// Rewind a private view; the live workspace remains untouched until the
	// replacement journal and its checksummed manifest have both been installed.
	base := *d
	base.value, base.templates, base.rules = maps.Clone(d.value), maps.Clone(d.templates), maps.Clone(d.rules)
	base.workspaces = slices.Clone(d.workspaces)
	for i := len(u) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		base.update(u[i], false)
		if u[i].NextBase != nil {
			base.value, base.templates, base.rules = maps.Clone(base.value), maps.Clone(base.templates), maps.Clone(base.rules)
		}
	}
	h := savedHistory{Revision: d.Revision, Base: base.Base, Structures: d.archivedStructures + len(base.workspaces), Undo: u, Redo: redo, Receipts: map[string]savedReceipt{}}
	for a, v := range base.value {
		h.Values = append(h.Values, Change{a, v})
	}
	for a, f := range base.templates {
		h.Templates = append(h.Templates, ColumnFormula{Address: a, Formula: f})
	}
	for _, v := range base.rules {
		h.Rules = append(h.Rules, v)
	}
	// Store recent idempotency receipts, including undone and branched requests.
	// An older retry outside the window still fails its original revision check.
	receipts := map[string]receipt{}
	for id, r := range d.receipts {
		if d.Revision-r.revision < formats.EditHistorySteps {
			receipts[id] = r
		}
	}
	for _, txs := range [][]transaction{u, redo} {
		for _, tx := range txs {
			receipts[tx.Request] = d.receipts[tx.Request]
		}
	}
	for id, r := range receipts {
		h.Receipts[id] = savedReceipt{r.digest, r.revision}
	}
	var out bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	if err := json.NewEncoder(&historyWriter{dest: zw, remaining: 4*formats.CheckpointBlockBytes - 1}).Encode(h); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	m, err := d.lease.ReplaceEditJournal(ctx, d.Revision, out.Bytes())
	if err != nil {
		return err
	}
	d.undo, d.redo = slices.Clone(u), slices.Clone(redo)
	d.workspaces = slices.Clone(d.workspaces[len(base.workspaces):])
	d.archivedStructures = h.Structures
	d.receipts = receipts
	d.dataSize = 0
	d.blocks = 1
	d.Updated = m.Updated
	d.refreshInfo()
	return nil
}

// Keep the writer and the bounded restore decoder on the same uncompressed limit.
type historyWriter struct {
	dest      io.Writer
	remaining int64
}

func (w *historyWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > w.remaining {
		return 0, checkpoint.ErrCapacity
	}
	n, err := w.dest.Write(b)
	w.remaining -= int64(n)
	return n, err
}

func (d *Document) restoreHistory(r io.Reader, revision uint64) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return checkpoint.ErrCorrupt
	}
	defer zr.Close()
	// Cap inflated data independently from the compressed checkpoint block.
	limited := &io.LimitedReader{R: zr, N: 4 * formats.CheckpointBlockBytes}
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	var h savedHistory
	if dec.Decode(&h) != nil || dec.Decode(new(json.RawMessage)) != io.EOF || limited.N == 0 || h.Revision != revision || h.Structures < 0 || len(h.Values) > formats.EditChangedCells || len(h.Rules) > formats.EditValidationRules || len(h.Undo) > retainedUndo || len(h.Redo) > retainedUndo || h.Base.Name != d.origin.Name || h.Base.Source.Format != d.origin.Source.Format || !json.Valid(h.Base.Layout) {
		return checkpoint.ErrCorrupt
	}
	d.Base = h.Base
	d.archivedStructures = h.Structures
	for _, c := range h.Values {
		if !validAddress(c.Address) || !c.Value.Valid() {
			return checkpoint.ErrCorrupt
		}
		d.value[c.Address] = c.Value
	}
	for _, c := range h.Templates {
		if !validAddress(c.Address) {
			return checkpoint.ErrCorrupt
		}
		d.templates[c.Address] = c.Formula
	}
	for _, v := range h.Rules {
		if !v.Valid() {
			return checkpoint.ErrCorrupt
		}
		d.rules[v.Area] = v
	}
	replay := func(tx transaction) error {
		r, ok := h.Receipts[tx.Request]
		if !ok || r.Digest != tx.Digest || r.Revision > revision || !d.validTransaction(tx) {
			return checkpoint.ErrCorrupt
		}
		d.apply(tx, r.Revision)
		return nil
	}
	for _, tx := range h.Undo {
		if err := replay(tx); err != nil {
			return err
		}
	}
	for i := len(h.Redo) - 1; i >= 0; i-- {
		if err := replay(h.Redo[i]); err != nil {
			return err
		}
	}
	for range h.Redo {
		i := len(d.undo) - 1
		tx := d.undo[i]
		d.undo = d.undo[:i]
		d.update(tx, false)
		d.redo = append(d.redo, tx)
	}
	for k, v := range h.Receipts {
		if !ValidRequestID(k) || !hexString(v.Digest, 32) || v.Revision < 2 || v.Revision > revision {
			return checkpoint.ErrCorrupt
		}
		d.receipts[k] = receipt{v.Digest, v.Revision}
	}
	return nil
}

func (d *Document) ReferencedBasePaths() map[string]bool {
	paths := map[string]bool{d.OriginalBase().Source.Path: true, d.Base.Source.Path: true}
	for _, w := range d.workspaces {
		paths[w.base.Source.Path] = true
	}
	for _, set := range [][]transaction{d.undo, d.redo} {
		for _, tx := range set {
			if tx.NextBase != nil {
				paths[tx.NextBase.Source.Path] = true
			}
		}
	}
	return paths
}
