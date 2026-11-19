package editfile

import (
	"context"
	"errors"
	"fmt"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

// Validate checks the complete proposed batch and returns the original values
// used for confirmation. Rules see the prospective value of each list cell,
// independent of the order of edits within the batch.
func (b EditBatch) Validate(ctx context.Context, changes []editdoc.Change, doc *editdoc.Document) (map[editdoc.Address]editdoc.Value, []editdoc.Change, error) {
	base := make(map[editdoc.Address]editdoc.Value, len(changes))
	kept := make([]editdoc.Change, 0, len(changes))
	var proposed map[editdoc.Address]editdoc.Value
	if len(b.rules) != 0 {
		proposed = make(map[editdoc.Address]editdoc.Value)
	}
	for _, c := range changes {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		source, ok := b.cells[c.Address]
		if !ok {
			return nil, nil, ErrTarget
		}
		if err := b.layout.validateChange(ctx, c.Address, c.Value, source); err != nil {
			return nil, nil, err
		}
		if b.layout.XLSX != nil {
			if err := b.layout.XLSX.checkCellEdit(c.Address, source.ReadOnly, source.protected, c.Value); err != nil {
				return nil, nil, err
			}
		}
		if b.layout.Text != nil && source.Value.Kind == "missing" {
			return nil, nil, errors.New("选区包含该记录中缺失的列，无法扩展原记录")
		}
		base[c.Address] = source.Value
		prior := source.Value
		if doc != nil {
			prior = doc.ValueAt(c.Address, prior)
		}
		next := c.Value.Apply(prior)
		if next == prior || prior.Kind == "missing" && next.Kind == "blank" && next.Style == prior.Style {
			continue
		}
		kept = append(kept, c)
		if proposed != nil && c.Value.Kind != "" {
			proposed[c.Address] = next
		}
	}
	for _, rule := range b.rules {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if rule.Reference {
			affected := false
			for addr := range proposed {
				if rule.Contains(addr) || rule.reference.Contains(addr) {
					affected = true
					break
				}
			}
			if !affected {
				continue
			}
			choices, err := b.choices(rule, doc, proposed)
			if err != nil {
				return nil, nil, err
			}
			rule.choices = choices
		}
		for _, c := range kept {
			if value, changed := proposed[c.Address]; changed && rule.Contains(c.Address) && !rule.accepts(value) {
				return nil, nil, fmt.Errorf("%s%d 的内容不符合数据规则", workbook.ColumnName(c.Col), c.Row)
			}
		}
	}
	return base, kept, nil
}
