package workbook

import (
	"encoding/xml"

	"excellens/internal/formats"
	"excellens/internal/styles"
)

// readRichTextStyled is opt-in. Ordinary values, indexes, scans and numeric
// interpretation keep using decodeRichText without allocating run metadata.
func (w *Workbook) readRichTextStyled(d *xml.Decoder, start xml.StartElement) (string, *RichText, error) {
	var runs []RichTextRun
	var notice string
	truncated := false
	offset := 0
	text, hasRuns, err := decodeRichTextContent(d, start, func(text string, font *styles.RichFontXML, inRun bool) {
		before := offset
		offset += styles.UTF16TextLength(text)
		if !inRun && len(runs) == 0 {
			return
		}
		if len(runs) == 0 && before > 0 {
			runs = append(runs, RichTextRun{End: before})
		}
		if truncated {
			return
		}
		resolved, complete := w.resolveRichFont(font)
		if !complete && notice == "" {
			notice = "部分富文本字体属性尚未还原，原文保留"
		}
		if len(runs) > 0 && styles.SameRichFont(runs[len(runs)-1].Font, resolved) {
			runs[len(runs)-1].End = offset
			return
		}
		if len(runs) >= formats.MaxCellRichRuns {
			truncated = true
			notice = "富文本样式片段超过显示容量，剩余原文保留"
			return
		}
		runs = append(runs, RichTextRun{End: offset, Font: resolved})
	})
	if err != nil {
		return "", nil, err
	}
	if !hasRuns {
		return text, nil, nil
	}
	if len(runs) == 0 && offset > 0 {
		runs = []RichTextRun{{End: offset}}
	}
	if notice == "" && (len(runs) == 0 || len(runs) == 1 && styles.SameRichFont(runs[0].Font, RichFont{})) {
		return text, nil, nil
	}
	return text, &RichText{Runs: runs, Notice: notice}, nil
}
