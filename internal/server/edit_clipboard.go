package server

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

const editClipboardFormat = "ExcelLens.EditSelection"

type editClipboardCell struct {
	Value   *editdoc.Value `json:"value,omitempty"`
	Formula string         `json:"formula,omitempty"`
	Style   string         `json:"style,omitempty"`
}

type editClipboard struct {
	Format  string              `json:"format"`
	Version int                 `json:"version"`
	Source  string              `json:"source,omitempty"`
	Start   editdoc.Address     `json:"start"`
	Rows    int                 `json:"rows"`
	Cols    int                 `json:"cols"`
	Cells   []editClipboardCell `json:"cells"`
	Proof   string              `json:"proof,omitempty"`
}

type editBatchPreview struct {
	Changes   []editdoc.Change `json:"changes,omitempty"`
	Last      editdoc.Address  `json:"last"`
	LastValue string           `json:"lastValue,omitempty"`
	Rule      *editdoc.Rule    `json:"rule,omitempty"`
	Proof     string           `json:"proof,omitempty"`
}

func editAddress(sheet, ref string) (editdoc.Address, error) {
	col, row, err := workbook.ParseCellRef(ref)
	if err != nil || sheet == "" || col > formats.ExcelColumns || row > formats.ExternalRows {
		return editdoc.Address{}, editfile.ErrTarget
	}
	return editdoc.Address{Sheet: sheet, Row: row, Col: col}, nil
}

func (a *App) editClipboardProof(clip editClipboard) string {
	clip.Proof = ""
	data, _ := json.Marshal(clip)
	return a.editTicket("clipboard-v2", string(data))
}

func (a *App) copyEditCells(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (editClipboard, error) {
	if req.Revision != doc.Revision {
		return editClipboard{}, checkpoint.ErrConflict
	}
	first, err := editAddress(req.Sheet, req.Ref)
	if err != nil {
		return editClipboard{}, err
	}
	last := first
	if req.End != "" {
		last, err = editAddress(req.Sheet, req.End)
		if err != nil {
			return editClipboard{}, err
		}
	}
	first.Row, last.Row = min(first.Row, last.Row), max(first.Row, last.Row)
	first.Col, last.Col = min(first.Col, last.Col), max(first.Col, last.Col)
	rows, cols := last.Row-first.Row+1, last.Col-first.Col+1
	if rows > formats.EditBatchCells/cols {
		return editClipboard{}, errEditRequestCapacity
	}
	addresses := make([]editdoc.Address, 0, rows*cols)
	for row := first.Row; row <= last.Row; row++ {
		for col := first.Col; col <= last.Col; col++ {
			addresses = append(addresses, editdoc.Address{Sheet: first.Sheet, Row: row, Col: col})
		}
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return editClipboard{}, err
	}
	defer f.Close()
	sources, err := layout.Read(ctx, f, addresses)
	if err != nil {
		return editClipboard{}, err
	}
	clip := editClipboard{Format: editClipboardFormat, Version: 2, Start: first, Rows: rows, Cols: cols, Cells: make([]editClipboardCell, 0, len(addresses))}
	size := 1024
	calc, err := a.calculationFor(ctx, doc, layout, f)
	if err != nil {
		return editClipboard{}, err
	}
	for _, addr := range addresses {
		source := sources[addr]
		value := doc.ValueAt(addr, source.Value)
		if derived, ok := calc.ArrayMembers[addr]; ok {
			value = derived
		}
		item := editClipboardCell{Style: value.Style}
		value.Style = ""
		if value.Kind == "formula" {
			item.Formula = value.Raw
			_, item.Value = calc.FormulaResult(addr, source)
		} else {
			item.Value = &value
		}
		if calc.GeneratedStatus(addr) != "" {
			item.Value = nil
		}
		data, _ := json.Marshal(item)
		size += len(data) + 1
		if size > formats.EditRequestBytes {
			return editClipboard{}, errEditRequestCapacity
		}
		clip.Cells = append(clip.Cells, item)
	}
	if layout.XLSX != nil {
		clip.Source = doc.Base.Source.Stamp
		clip.Proof = a.editClipboardProof(clip)
	}
	return clip, nil
}

func (a *App) editPlanProof(doc *editdoc.Document, plan any) string {
	data, _ := json.Marshal(plan)
	return a.editTicket("edit-plan\x00"+doc.ID+"\x00"+doc.Binding+"\x00"+strconv.FormatUint(doc.Revision, 10), string(data))
}

func (a *App) validEditProof(doc *editdoc.Document, proof string, plan any) bool {
	return hmac.Equal([]byte(proof), []byte(a.editPlanProof(doc, plan)))
}

func (a *App) checkEditStyleProof(doc *editdoc.Document, proof string, changes []editdoc.Change) error {
	for _, c := range changes {
		if c.Value.Style != "" {
			if !a.validEditProof(doc, proof, changes) {
				return errors.New("格式变更需要当前修订的预览，请重新预览")
			}
			break
		}
	}
	return nil
}

func (a *App) previewEditCells(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) (editBatchPreview, error) {
	if req.Revision != doc.Revision {
		return editBatchPreview{}, checkpoint.ErrConflict
	}
	if req.Mode == "rule" {
		return a.previewEditRule(ctx, doc, layout, req.Rule)
	}
	first, err := editAddress(req.Sheet, req.Ref)
	if err != nil {
		return editBatchPreview{}, err
	}
	var preview editBatchPreview
	var targets, anchors []editdoc.Address
	fill := req.Mode == "date" || req.Mode == "time" || req.Mode == "formula-fill"
	if req.Mode == "changes" {
		last, err := editAddress(req.Sheet, req.End)
		if err != nil || !layout.Contains(first) || !layout.Contains(last) || len(req.Operation.Changes) == 0 || len(req.Operation.Changes) > formats.EditBatchCells {
			return preview, editfile.ErrTarget
		}
		seen := make(map[editdoc.Address]bool, len(req.Operation.Changes))
		for _, c := range req.Operation.Changes {
			if c.Sheet != first.Sheet || c.Row < min(first.Row, last.Row) || c.Row > max(first.Row, last.Row) ||
				c.Col < min(first.Col, last.Col) || c.Col > max(first.Col, last.Col) || !c.Value.Valid() || c.Value.Style != "" || seen[c.Address] {
				return preview, editdoc.ErrInvalid
			}
			seen[c.Address] = true
		}
		preview = editBatchPreview{Changes: req.Operation.Changes, Last: last}
		for _, c := range preview.Changes {
			if c.Address == last {
				preview.LastValue = c.Value.Raw
			}
		}
	} else if fill {
		targets, err = editFillTargets(layout, first, req.End)
		anchors = []editdoc.Address{first}
	} else {
		preview, err = a.planClipboard(ctx, doc, layout, first, req)
	}
	if err != nil {
		return preview, err
	}
	if !fill {
		targets = editChangeAddresses(preview.Changes)
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return preview, err
	}
	defer f.Close()
	tableEdits := layout.XLSX != nil && len(layout.XLSX.Tables) > 0
	var batch editfile.EditBatch
	if !tableEdits || fill {
		batchTargets := targets
		if tableEdits {
			// Table planning validates all effective targets after expanding
			// the column. Only the fill anchor's current value is needed here.
			batchTargets = nil
		}
		batch, err = layout.ReadBatch(ctx, f, batchTargets, doc.RuleChanges(), anchors...)
		if err != nil {
			return preview, err
		}
	}
	if fill {
		start := doc.ValueAt(first, batch.SourceValue(first))
		preview, err = planEditFill(ctx, layout, f, first, start, targets, req)
		if err != nil {
			return preview, err
		}
	}
	var kept []editdoc.Change
	if tableEdits {
		plan, planErr := layout.PrepareChanges(ctx, f, preview.Changes, doc)
		kept, err = plan.Changes, planErr
	} else {
		_, kept, err = batch.Validate(ctx, preview.Changes, doc)
	}
	if err != nil {
		return preview, err
	}
	preview.Changes = kept
	if len(kept) == 0 {
		return preview, editdoc.ErrNoChange
	}
	data, _ := json.Marshal(kept)
	if len(data)+1024 > formats.EditRequestBytes {
		return preview, errEditRequestCapacity
	}
	for _, c := range kept {
		if c.Value.Style != "" {
			preview.Proof = a.editPlanProof(doc, kept)
			break
		}
	}
	return preview, nil
}

func (a *App) previewEditRule(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, input *editdoc.Rule) (editBatchPreview, error) {
	if input == nil || layout.XLSX == nil || !input.Valid() {
		return editBatchPreview{}, editdoc.ErrInvalid
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return editBatchPreview{}, err
	}
	defer f.Close()
	rule := *input
	if rule.Reference {
		rule.First = strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(rule.First), "="))
	} else if rule.Type == "list" {
		if strings.Contains(rule.First, ",") {
			return editBatchPreview{}, errors.New("列表选项每行一项，选项中不能含逗号")
		}
		choices := strings.Split(strings.ReplaceAll(rule.First, "\r\n", "\n"), "\n")
		rule.First = editfile.ListFormula(choices)
	} else if rule.Type == "date" {
		rule, err = editfile.DateRuleBounds(ctx, f, rule)
		if err != nil {
			return editBatchPreview{}, err
		}
	}
	overlays := doc.RuleChanges()
	if _, err := layout.RuleBase(rule, overlays); err != nil {
		return editBatchPreview{}, err
	}
	if err := layout.CheckRuleReference(ctx, f, rule, overlays, doc.ValueAt); err != nil {
		return editBatchPreview{}, err
	}
	last := editdoc.Address{Sheet: rule.Start.Sheet, Row: rule.EndRow, Col: rule.EndCol}
	return editBatchPreview{Rule: &rule, Last: last, Proof: a.editPlanProof(doc, rule)}, nil
}

func (a *App) planClipboard(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, first editdoc.Address, req editRequest) (editBatchPreview, error) {
	clip := req.Clipboard
	if clip == nil || clip.Format != editClipboardFormat || clip.Version != 2 || clip.Cols <= 0 || clip.Cols > formats.ExcelColumns ||
		clip.Rows <= 0 || clip.Rows > formats.EditBatchCells/clip.Cols || len(clip.Cells) != clip.Rows*clip.Cols ||
		clip.Start.Row < 1 || clip.Start.Row > formats.ExternalRows-clip.Rows+1 || clip.Start.Col < 1 || clip.Start.Col > formats.ExcelColumns-clip.Cols+1 {
		return editBatchPreview{}, errors.New("请选择从编辑副本复制的有类型数据")
	}
	switch req.Mode {
	case "values":
	case "formulas":
		if layout.XLSX == nil {
			return editBatchPreview{}, errors.New("此格式只能保存文本字段")
		}
	case "styles":
		if layout.XLSX == nil || clip.Source != doc.Base.Source.Stamp || !hmac.Equal([]byte(clip.Proof), []byte(a.editClipboardProof(*clip))) {
			return editBatchPreview{}, errors.New("仅格式粘贴需要从同一工作簿重新复制")
		}
	default:
		return editBatchPreview{}, editdoc.ErrInvalid
	}
	last := editdoc.Address{Sheet: first.Sheet, Row: first.Row + clip.Rows - 1, Col: first.Col + clip.Cols - 1}
	if !layout.Contains(first) || !layout.Contains(last) {
		return editBatchPreview{}, editfile.ErrTarget
	}
	preview := editBatchPreview{Changes: make([]editdoc.Change, 0, len(clip.Cells)), Last: last}
	for i, c := range clip.Cells {
		r, col := i/clip.Cols, i%clip.Cols
		target := editdoc.Address{Sheet: first.Sheet, Row: first.Row + r, Col: first.Col + col}
		var v editdoc.Value
		switch req.Mode {
		case "values":
			if c.Value == nil {
				return preview, fmt.Errorf("%s%d 当前没有可复制的结果", workbook.ColumnName(clip.Start.Col+col), clip.Start.Row+r)
			}
			v = *c.Value
			if v.Kind == "missing" {
				v = editdoc.Value{Kind: "blank"}
			}
			if v.Style != "" || v.Kind == "formula" || !v.Valid() {
				return preview, editdoc.ErrInvalid
			}
			if req.SkipBlank && v.Kind == "blank" {
				continue
			}
		case "formulas":
			if c.Formula == "" {
				continue
			}
			formula, err := layout.XLSX.FormulaForFill(ctx, c.Formula)
			if err != nil {
				return preview, err
			}
			expression, err := formula.Shift(first.Row-clip.Start.Row, first.Col-clip.Start.Col)
			if err != nil {
				return preview, err
			}
			v = editdoc.Value{Kind: "formula", Raw: expression}
		case "styles":
			v.Style = c.Style
		}
		preview.Changes = append(preview.Changes, editdoc.Change{Address: target, Value: v})
	}
	if len(preview.Changes) == 0 {
		return preview, editdoc.ErrNoChange
	}
	return preview, nil
}
