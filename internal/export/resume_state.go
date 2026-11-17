package export

import (
	"context"
	"errors"
	"io"
	"time"

	"excellens/internal/formats"
)

var ErrResumeState = errors.New("导出恢复状态与当前范围不一致，请重新运行")

type ResumeState struct {
	Version int    `json:"version"`
	Phase   string `json:"phase"`
	Sheet   int    `json:"sheet"`
	Row     int64  `json:"row,string"`
	Notices int64  `json:"notices,string"`
	Commits uint64 `json:"commits,string"`
}

type ResumePart struct {
	Before ResumeState `json:"before"`
	Count  int64       `json:"count,string"`
}

type ResumeSheet struct {
	Rows    int64
	Columns int
}

// The journal owns persistence, source binding and complete-part transactions.
// Visit exposes private output only; no download is published before it and
// the caller's source checks succeed. No implementation may replay produce
// after an uncertain write; rebuild a fresh driver from the confirmed state.
type ResumeJournal interface {
	State() ResumeState
	Created() time.Time
	SourceName() string
	Commit(context.Context, ResumePart, func(io.Writer) (ResumeState, error)) error
	Visit(context.Context, func(ResumePart, io.Reader) error) error
}

func InitialResumeState(opts Options) ResumeState {
	phase := "text"
	switch opts.Format {
	case "html":
		phase = "html-head"
	case "xlsx":
		phase = "xlsx-sheet"
	case "zipcsv":
		phase = "zipcsv-sheet"
	}
	return ResumeState{Version: 1, Phase: phase}
}

func ValidateResumeState(s ResumeState, opts Options, selections []Selection) error {
	shape := make([]ResumeSheet, len(selections))
	for i, selection := range selections {
		shape[i] = ResumeSheet{Rows: selection.RowCount(), Columns: selection.LastCol - selection.FirstCol + 1}
	}
	return ValidateResumeShape(s, opts, shape)
}

// Metadata-only validation does not open a source or allocate row vectors.
func ValidateResumeShape(s ResumeState, opts Options, selections []ResumeSheet) error {
	if ValidateOptions(opts) != nil || len(selections) == 0 || s.Version != 1 || s.Sheet < 0 || s.Row < 0 || s.Notices < 0 {
		return ErrResumeState
	}
	parts := func(rows int64) uint64 {
		return uint64((rows + formats.ExportRecoveryRows - 1) / formats.ExportRecoveryRows)
	}
	if opts.Format == "xlsx" || opts.Format == "zipcsv" {
		phase := InitialResumeState(opts).Phase
		if s.Sheet == len(selections) {
			phase = "done"
		}
		if s.Sheet > len(selections) || s.Row != 0 || s.Notices != 0 || s.Commits != uint64(s.Sheet) || s.Phase != phase {
			return ErrResumeState
		}
		return nil
	}
	rows := selections[0].Rows
	if len(selections) != 1 || s.Sheet != 0 || s.Row > rows {
		return ErrResumeState
	}
	if opts.Format != "html" {
		expected := parts(s.Row)
		if s.Phase == "done" && rows == 0 {
			expected = 1
		}
		if s.Notices != 0 || s.Commits != expected || s.Phase != "text" && s.Phase != "done" || s.Phase == "done" && s.Row != rows || s.Phase == "text" && (s.Row%formats.ExportRecoveryRows != 0 || rows > 0 && s.Row == rows) {
			return ErrResumeState
		}
		return nil
	}
	if s.Notices > s.Row*int64(selections[0].Columns) {
		return ErrResumeState
	}
	switch s.Phase {
	case "html-head":
		if s.Row != 0 || s.Commits != 0 || s.Notices != 0 {
			return ErrResumeState
		}
	case "html-rows":
		if s.Row >= rows || s.Row%formats.ExportRecoveryRows != 0 || s.Commits != 1+parts(s.Row) {
			return ErrResumeState
		}
	case "html-tail", "done":
		extra := uint64(1)
		if s.Phase == "done" {
			extra++
		}
		if s.Row != rows || s.Commits != extra+parts(rows) {
			return ErrResumeState
		}
	default:
		return ErrResumeState
	}
	return nil
}

func NextResumeState(part ResumePart, notices int64, opts Options, selections []Selection) (ResumeState, error) {
	s := part.Before
	if ValidateResumeState(s, opts, selections) != nil || s.Phase == "done" || notices < 0 {
		return s, ErrResumeState
	}
	expected := int64(0)
	switch s.Phase {
	case "text", "html-rows":
		expected = min(int64(formats.ExportRecoveryRows), selections[0].RowCount()-s.Row)
	case "xlsx-sheet", "zipcsv-sheet":
		expected = selections[s.Sheet].RowCount()
	}
	if part.Count != expected || notices > 0 && s.Phase != "html-rows" || notices > expected*int64(selections[min(s.Sheet, len(selections)-1)].LastCol-selections[min(s.Sheet, len(selections)-1)].FirstCol+1) {
		return s, ErrResumeState
	}
	s.Commits++
	s.Notices += notices
	switch s.Phase {
	case "text", "html-rows":
		s.Row += expected
		if s.Row == selections[0].RowCount() {
			s.Phase = "done"
			if opts.Format == "html" {
				s.Phase = "html-tail"
			}
		}
	case "html-head":
		s.Phase = "html-rows"
		if selections[0].RowCount() == 0 {
			s.Phase = "html-tail"
		}
	case "html-tail":
		s.Phase = "done"
	case "xlsx-sheet", "zipcsv-sheet":
		s.Sheet++
		if s.Sheet == len(selections) {
			s.Phase = "done"
		}
	}
	if err := ValidateResumeState(s, opts, selections); err != nil {
		return s, err
	}
	return s, nil
}

func ValidateResumeTransition(part ResumePart, after ResumeState, opts Options, selections []Selection) error {
	expected, err := NextResumeState(part, after.Notices-part.Before.Notices, opts, selections)
	if err != nil || expected != after {
		return ErrResumeState
	}
	return nil
}

func ResumeDoneRows(s ResumeState, selections []Selection) int64 {
	rows := s.Row
	for i := 0; i < min(s.Sheet, len(selections)); i++ {
		rows += selections[i].RowCount()
	}
	return rows
}
