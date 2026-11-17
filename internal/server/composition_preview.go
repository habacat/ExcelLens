package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"excellens/internal/composition"
	"excellens/internal/formats"
	"excellens/internal/sourceview"
)

// The compact preview carries exact data and provenance, not a second native
// rendering model. Styles/formulas are never interpreted at result positions.
type compositionPreviewCell struct {
	Column        int    `json:"column"`
	SourceColumn  int    `json:"sourceColumn"`
	Exists        bool   `json:"exists"`
	Ref           string `json:"ref"`
	Raw           string `json:"raw"`
	Display       string `json:"display"`
	Type          string `json:"type,omitempty"`
	Formula       string `json:"formula,omitempty"`
	HasFormula    bool   `json:"hasFormula"`
	CacheMissing  bool   `json:"cacheMissing,omitempty"`
	SharedFormula bool   `json:"sharedFormula,omitempty"`
	Style         int    `json:"style"`
	ValueColor    string `json:"valueColor,omitempty"`
}

type compositionPreviewRecord struct {
	Sequence int64                    `json:"sequence"`
	Input    int                      `json:"input"`
	Row      int                      `json:"row"`
	Position int                      `json:"position"`
	Cells    []compositionPreviewCell `json:"cells"`
}

type compositionTaskResult struct {
	ID               string                     `json:"id"`
	Mode             string                     `json:"mode"`
	WorkbookKey      string                     `json:"workbookKey"`
	Book             string                     `json:"book"`
	Source           string                     `json:"source"`
	Definition       *sourceview.Append         `json:"definition,omitempty"`
	Info             *compositionSourceInfo     `json:"info,omitempty"`
	Sources          []compositionSourceInfo    `json:"sources,omitempty"`
	Rows             int64                      `json:"rows"`
	Preview          []compositionPreviewRecord `json:"preview,omitempty"`
	PreviewTruncated bool                       `json:"previewTruncated"`
}

var errCompositionPreviewComplete = errors.New("composition preview is full")

func compositionPreview(ctx context.Context, source *compositionSnapshot) ([]compositionPreviewRecord, bool, error) {
	return compositionPreviewRows(ctx, source, formats.CompositionPreviewRows)
}

func compositionPreviewRows(ctx context.Context, source *compositionSnapshot, limit int) ([]compositionPreviewRecord, bool, error) {
	columns := make([]int, len(source.data.plan.Definition().Fields))
	for i := range columns {
		columns[i] = i + 1
	}
	preview := make([]compositionPreviewRecord, 0, limit)
	remaining := formats.CompositionPreviewBytes
	err := source.scan(ctx, columns, func(row composition.Record) error {
		if len(preview) == limit {
			return errCompositionPreviewComplete
		}
		// Count escaped string contents before allocating cloned/encoded copies.
		// Short selected values must not retain an entire large source row buffer.
		textBudget := remaining - 128 - len(row.Cells)*256
		if textBudget < 0 {
			return errCompositionPreviewComplete
		}
		for _, cell := range row.Cells {
			for _, text := range []string{cell.Data.Ref, cell.Data.Raw, cell.Data.Display, cell.Data.Type, cell.Data.Formula, cell.Data.ValueColor} {
				if !jsonTextFits(text, &textBudget) {
					return errCompositionPreviewComplete
				}
			}
		}
		out := compositionPreviewRecord{Sequence: row.Sequence, Input: row.Input, Row: row.Row, Position: row.Position, Cells: make([]compositionPreviewCell, len(row.Cells))}
		for i, cell := range row.Cells {
			data := cell.Data
			out.Cells[i] = compositionPreviewCell{Column: cell.Column, SourceColumn: cell.SourceColumn, Exists: cell.Exists, Ref: strings.Clone(data.Ref), Raw: strings.Clone(data.Raw), Display: strings.Clone(data.Display), Type: strings.Clone(data.Type), Formula: strings.Clone(data.Formula), HasFormula: data.HasFormula, CacheMissing: data.CacheMissing, SharedFormula: data.SharedFormula, Style: data.Style, ValueColor: strings.Clone(data.ValueColor)}
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if len(encoded)+1 > remaining {
			return errCompositionPreviewComplete
		}
		remaining -= len(encoded) + 1
		preview = append(preview, out)
		if len(preview) == limit && source.data.plan.Rows() > int64(len(preview)) {
			return errCompositionPreviewComplete
		}
		return nil
	})
	if err != nil && !errors.Is(err, errCompositionPreviewComplete) {
		return nil, false, err
	}
	// Stopping a bounded preview does not mark the entire input as scanned.
	// Every source identity is still checked before publishing that preview.
	if err := source.data.plan.CheckSources(ctx); err != nil {
		return nil, false, err
	}
	return preview, int64(len(preview)) < source.data.plan.Rows(), nil
}
