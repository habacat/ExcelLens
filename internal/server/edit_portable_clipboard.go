package server

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"excellens/internal/calculator"
	"strings"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
)

type portableClipboard struct {
	Range *editdoc.PortableRange `json:"range"`
	Proof string                 `json:"proof"`
}

func (a *App) signedRange(r *editdoc.PortableRange) (*portableClipboard, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if len(data)+4096 > formats.EditRequestBytes {
		return nil, errEditRequestCapacity
	}
	return &portableClipboard{r, a.editTicket("portable-range", string(data))}, nil
}
func (a *App) nativePaste(ctx context.Context, d *editdoc.Document, req editRequest) (*portableClipboard, error) {
	if req.Revision != d.Revision {
		return nil, checkpoint.ErrConflict
	}
	h, err := hex.DecodeString(req.Proof)
	if err != nil || len(h) != 32 {
		return nil, editdoc.ErrInvalid
	}
	data, sheet, err := nativeClipboard(ctx, req.Proof)
	if err != nil || data == nil {
		return nil, err
	}
	r, err := editfile.NativeClipboardRange(ctx, data, sheet)
	if err != nil {
		return nil, errors.New("不能读取 Excel 复制区域，可用 Ctrl+Shift+V 粘贴文字。" + err.Error())
	}
	return a.signedRange(r)
}

func (a *App) previewPortable(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (editBatchPreview, error) {
	var plan editBatchPreview
	op := req.Operation.Structure
	if layout.XLSX == nil || op == nil || op.Kind != "pasteRange" || op.Paste == nil {
		return plan, editdoc.ErrInvalid
	}
	if err := a.checkPortable(ctx, doc, op.Paste, req.Proof); err != nil {
		return plan, err
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return plan, err
	}
	defer f.Close()
	plan.Changes, err = layout.XLSX.PortableGridChanges(ctx, f, *op)
	if err != nil {
		return plan, err
	}
	if len(plan.Changes) == 0 {
		plan.Structure = true
	} else {
		plan.Proof = a.editPlanProof(doc, plan.Changes)
	}
	return plan, nil
}

// Excel creates a source-workbook link for any explicit sheet qualifier,
// even when the destination has a worksheet of that name. Never silently
// bind it or a workbook-scoped name to unrelated values in the target.
func (a *App) checkPortable(ctx context.Context, doc *editdoc.Document, r *editdoc.PortableRange, proof string) error {
	if r == nil {
		return editdoc.ErrInvalid
	}
	signed, err := a.signedRange(r)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(signed.Proof), []byte(proof)) {
		return errors.New("剪贴板格式已过期，请重新复制")
	}
	if r.Origin == doc.OriginalBase().Source.Book {
		return nil
	}
	examined := map[string]bool{}
	for _, cell := range r.Cells {
		raw := cell.Value.Raw
		if cell.Value.Kind != "formula" || examined[raw] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		examined[raw] = true
		bound := false
		_, err := calculator.RewriteReferences(raw, func(sheet, q, ref string) (string, error) { bound = bound || sheet != ""; return q + ref, nil })
		if err != nil {
			return err
		}
		_, _ = calculator.ParseLocalFormula(ctx, strings.TrimPrefix(raw, "="), func(string) bool { return true }, func(string) (bool, bool) { bound = true; return false, true })
		if bound {
			return errors.New("此公式需要链接来源工作簿或导入其名称，可用 Ctrl+Shift+V 粘贴显示值")
		}
	}
	return nil
}
