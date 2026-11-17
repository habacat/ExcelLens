package export

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type resumePalette struct {
	Version     int            `json:"version"`
	Initialized bool           `json:"initialized"`
	BaseStyles  int            `json:"baseStyles"`
	Variants    map[string]int `json:"variants"`
	Components  map[string]int `json:"components"`
}

func writeResumeXLSXPart(ctx context.Context, dst io.Writer, wb *workbook.Workbook, s Selection, opts Options, tick Tick, palette *xlsxStylePalette) error {
	sheet := NewPartWriter(dst)
	if err := writeXLSXSheet(ctx, sheet, wb, s, opts, tick, palette); err != nil {
		return err
	}
	if err := sheet.Close(); err != nil {
		return err
	}
	styles := NewPartWriter(dst)
	if err := palette.write(styles); err != nil {
		return err
	}
	if err := styles.Close(); err != nil {
		return err
	}
	metadata := NewPartWriter(dst)
	state := resumePalette{Version: 1, Initialized: palette.doc != nil, BaseStyles: palette.baseStyles, Variants: palette.variants, Components: palette.components}
	if err := json.NewEncoder(metadata).Encode(state); err != nil {
		return err
	}
	return metadata.Close()
}

func readResumePalette(ctx context.Context, source io.Reader) (*xlsxStylePalette, error) {
	if _, err := io.Copy(io.Discard, NewPartReader(source)); err != nil {
		return nil, err
	}
	styles := NewPartReader(source)
	raw, err := io.ReadAll(io.LimitReader(styles, formats.XLSXStylePartBytes+1))
	if err != nil || len(raw) > formats.XLSXStylePartBytes {
		return nil, ErrResumeState
	}
	metadata := NewPartReader(source)
	limited := &io.LimitedReader{R: metadata, N: formats.XLSXStyleWorkBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var state resumePalette
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || limited.N == 0 || state.Version != 1 || len(state.Variants) > formats.XLSXStyleRecords || len(state.Components) > 3*formats.XLSXStyleRecords {
		return nil, ErrResumeState
	}
	p := newXLSXStylePalette(ctx, string(raw))
	if !state.Initialized {
		if state.BaseStyles != 0 || len(state.Variants) != 0 || len(state.Components) != 0 {
			return nil, ErrResumeState
		}
		return p, nil
	}
	if err := p.ensure(); err != nil {
		return nil, err
	}
	if state.BaseStyles < 1 || state.BaseStyles > p.doc.section("cellXfs").count() || state.Variants == nil || state.Components == nil {
		return nil, ErrResumeState
	}
	for key, id := range state.Variants {
		if id < state.BaseStyles || id >= p.doc.section("cellXfs").count() {
			return nil, ErrResumeState
		}
		if err := p.doc.reserve(len(key)*2 + 96); err != nil {
			return nil, err
		}
	}
	for key, id := range state.Components {
		kind, _, found := strings.Cut(key, ":")
		section := map[string]string{"font": "fonts", "fill": "fills", "border": "borders"}[kind]
		if !found || section == "" || id < 0 || id >= p.doc.section(section).count() {
			return nil, ErrResumeState
		}
		if err := p.doc.reserve(len(key)*2 + 96); err != nil {
			return nil, err
		}
	}
	p.baseStyles, p.variants, p.components = state.BaseStyles, state.Variants, state.Components
	return p, nil
}
