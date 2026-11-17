package workbook

import (
	"encoding/json"
	"excellens/internal/formats"
)

type richReadBudget struct{ remaining int }

func (b *richReadBudget) admit(bytes int) bool {
	if bytes < 0 || bytes > b.remaining {
		return false
	}
	b.remaining -= bytes
	return true
}
func richReadLimitNotice() *RichText {
	return &RichText{Notice: "富文本样式超过本次显示容量，原文完整保留"}
}
func prepareRichRead(options RowReadOptions) RowReadOptions {
	if options.RichText && options.richBudget == nil {
		options.richBudget = &richReadBudget{remaining: formats.RichReadBytes}
	}
	return options
}

func (w *Workbook) resolveCellWithPresentation(source xmlCell, col int, anchors map[string]SharedFormulaAnchor, options RowReadOptions) CellData {
	cell := w.resolveCell(source, col, anchors, options.FormatValue, options.IncludeFormula)
	if !options.RichText {
		return cell
	}
	if source.Type == "inlineStr" {
		cell.RichText = source.RichText
		if cell.RichText != nil && options.richBudget != nil {
			encoded, err := json.Marshal(cell.RichText)
			if err != nil || !options.richBudget.admit(len(encoded)) {
				cell.RichText = richReadLimitNotice()
			}
		}
	}
	if source.Type == "s" && w.richShared != nil {
		if id, err := parseNonNegativeInt(source.Value); err == nil && id < len(w.Shared) {
			rich, err := w.richShared.lookupWithBudget(id, cell.Raw, options.richBudget)
			if err != nil {
				cell.RichText = &RichText{Notice: "富文本样式不可用，原文保留：" + err.Error()}
			} else {
				cell.RichText = rich
			}
		}
	}
	return cell
}
