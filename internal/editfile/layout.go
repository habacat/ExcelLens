package editfile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type Layout struct {
	Text     *TextLayout `json:"text,omitempty"`
	XLSX     *XLSXLayout `json:"xlsx,omitempty"`
	cache    *SourceCellCache
	cacheKey string
	index    *CalculationBook
}

type ArrayOwner struct {
	Formula string `json:"formula,omitempty"`
	Range   string `json:"range"`
	Dynamic bool   `json:"dynamic"`
}

type SourceCell struct {
	Array     *ArrayOwner
	protected bool
	rules     []validatedRule
	Value     editdoc.Value
	Cache     *editdoc.Value
	ReadOnly  string
	formula   sourceFormulaTag
	names     map[nameKey]nameTarget
}

// FillStyle copies the displayed style within a worksheet. A protected
// worksheet keeps each destination's own style, including its lock bit.
func (c SourceCell) FillStyle(current editdoc.Value) string {
	if c.protected {
		return ""
	}
	if current.Style == "" {
		return "0"
	}
	return current.Style
}

func (l Layout) Contains(a editdoc.Address) bool {
	if l.Text != nil {
		return a.Sheet == l.Text.Sheet && a.Row >= 1 && a.Row <= l.Text.Rows && a.Col >= 1 && a.Col <= l.Text.Columns
	}
	for _, sheet := range l.XLSX.Sheets {
		if a.Sheet == sheet.Name {
			return a.Row >= 1 && a.Row <= formats.ExcelRows && a.Col >= 1 && a.Col <= formats.ExcelColumns
		}
	}
	return false
}

func ParseLayout(data json.RawMessage, format string) (Layout, error) {
	var layout Layout
	if err := json.Unmarshal(data, &layout); err != nil {
		return layout, ErrBinding
	}
	switch format {
	case "xlsx", "xlsm":
		if layout.XLSX != nil && layout.Text == nil && layout.XLSX.Valid() && layout.XLSX.MacroEnabled == (format == "xlsm") {
			return layout, nil
		}
	case "csv", "tsv":
		if layout.Text != nil && layout.XLSX == nil && layout.Text.Valid() {
			return layout, nil
		}
	}
	return Layout{}, ErrBinding
}

func Inspect(ctx context.Context, wb *workbook.Workbook, format string) (Layout, string, error) {
	f, err := workbook.OpenEditSource(wb.Path)
	if err != nil {
		return Layout{}, "", err
	}
	defer f.Close()
	if err := wb.CheckEditSource(ctx, f); err != nil {
		return Layout{}, "", err
	}
	if formats.EditOOXML(format) {
		l, proof, err := InspectXLSX(ctx, f, wb)
		return Layout{XLSX: &l}, proof, err
	}
	l, proof, err := InspectText(ctx, f, wb)
	return Layout{Text: &l}, proof, err
}

func (l Layout) Read(ctx context.Context, f *workbook.EditSourceFile, targets []editdoc.Address) (map[editdoc.Address]SourceCell, error) {
	if l.Text != nil {
		values, err := ReadTextCells(ctx, f, *l.Text, targets)
		if err != nil {
			return nil, err
		}
		cells := make(map[editdoc.Address]SourceCell, len(values))
		for address, value := range values {
			cells[address] = SourceCell{Value: value}
		}
		return cells, nil
	}
	if l.cache != nil {
		return l.cache.read(ctx, f, l, targets)
	}
	return ReadXLSXCells(ctx, f, *l.XLSX, targets)
}

func (l Layout) validateChange(ctx context.Context, a editdoc.Address, value editdoc.Value, source SourceCell) error {
	if l.Text == nil {
		return l.XLSX.validateValue(ctx, a, value, source)
	}
	if !value.Valid() || value.Style != "" || value.Kind != "blank" && (value.Kind != "text" || value.Raw == "") {
		return errors.New("分隔文本只保存文本字段")
	}
	return nil
}

// EditBatch contains one read of the source cells used by a single editor
// operation. Fill anchors and referenced validation lists share its read.
type EditBatch struct {
	layout Layout
	cells  map[editdoc.Address]SourceCell
	rules  []validatedRule
}

func (l Layout) ReadBatch(ctx context.Context, f *workbook.EditSourceFile, targets []editdoc.Address, overlays []editdoc.Rule, anchors ...editdoc.Address) (EditBatch, error) {
	batch := EditBatch{layout: l}
	for _, rule := range l.EffectiveRules(overlays) {
		affected := slices.ContainsFunc(targets, rule.Contains)
		if !affected && !rule.Reference {
			continue
		}
		compiled, err := compileRule(rule)
		if err != nil {
			return batch, err
		}
		if rule.Reference && !l.containsReference(compiled.reference) {
			return batch, errReferenceTarget
		}
		if affected || slices.ContainsFunc(targets, compiled.reference.Contains) {
			batch.rules = append(batch.rules, compiled)
		}
	}
	addresses, err := appendReferences(append(slices.Clone(targets), anchors...), batch.rules)
	if err != nil {
		return batch, err
	}
	batch.cells, err = l.Read(ctx, f, addresses)
	if err == nil && l.XLSX != nil && l.XLSX.Native {
		seen := map[editdoc.Rule]bool{}
		for _, cell := range batch.cells {
			for _, rule := range cell.rules {
				if !seen[rule.Rule] {
					seen[rule.Rule] = true
					batch.rules = append(batch.rules, rule)
				}
			}
		}
		more, refErr := l.readOfficeListMembers(ctx, f, batch.rules)
		if refErr != nil {
			return batch, refErr
		}
		more = slices.DeleteFunc(more, func(a editdoc.Address) bool { _, ok := batch.cells[a]; return ok })
		if len(more) > 0 {
			extra, readErr := l.Read(ctx, f, more)
			if readErr != nil {
				return batch, readErr
			}
			for a, c := range extra {
				batch.cells[a] = c
			}
		}
	}
	return batch, err
}

func (b EditBatch) SourceValue(address editdoc.Address) editdoc.Value { return b.cells[address].Value }

func (l Layout) Write(ctx context.Context, dst io.Writer, f *workbook.EditSourceFile, proof string, values map[editdoc.Address]editdoc.Value, rules []editdoc.Rule, templates map[editdoc.Address]string, options TextOptions, receipt *Artifact) (func(*os.File) error, error) {
	if l.Text != nil {
		if len(rules) > 0 {
			return nil, ErrBinding
		}
		result, err := WriteText(ctx, dst, f, *l.Text, proof, values, options)
		if err != nil {
			return nil, err
		}
		receipt.Encoding, receipt.Protected, receipt.Delimiter = result.Layout.Encoding, result.Protected, result.Layout.Delimiter
		return func(src *os.File) error { return VerifyText(ctx, src, result) }, nil
	}
	result, err := writeXLSX(ctx, dst, f, *l.XLSX, proof, values, rules, templates)
	if err != nil {
		return nil, err
	}
	receipt.ChangedParts = len(result.Changed)
	receipt.FormulasPending = result.FormulasPending
	receipt.NativeRecalc = result.NativeRecalc
	receipt.PivotCachesPending = result.PivotCachesPending
	receipt.PivotCachesAfterCalc = result.PivotCachesAfterCalc
	return func(src *os.File) error { return VerifyXLSX(ctx, src, result) }, nil
}
