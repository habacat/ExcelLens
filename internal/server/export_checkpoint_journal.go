package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"time"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type exportCheckpointJournal struct {
	wb         *workbook.Workbook
	lease      *checkpoint.Lease
	manifest   checkpoint.Manifest
	spec       exportCheckpointSpec
	cursor     exportTaskCursor
	selections []excelExport.Selection
}

func newExportCheckpointJournal(ctx context.Context, wb *workbook.Workbook, lease *checkpoint.Lease, selections []excelExport.Selection) (*exportCheckpointJournal, error) {
	m := lease.Snapshot()
	spec, err := exportCheckpointSpecFor(m)
	if err != nil {
		return nil, err
	}
	cursor, err := exportCheckpointCursor(m, spec)
	if err != nil {
		return nil, err
	}
	if !cursor.Snapshot {
		return nil, errExportCaptureIncomplete
	}
	if err := checkExportCheckpointSources(ctx, wb, m.Definition, spec, selections); err != nil {
		return nil, err
	}
	if cursor.Output == nil {
		cursor.InputBlocks = m.Blocks // legacy ready cursors did not store this
	}
	return &exportCheckpointJournal{wb: wb, lease: lease, manifest: m, spec: spec, cursor: cursor, selections: selections}, nil
}

func (j *exportCheckpointJournal) State() excelExport.ResumeState {
	if j.cursor.Output == nil {
		return excelExport.InitialResumeState(j.spec.Options)
	}
	return *j.cursor.Output
}

func (j *exportCheckpointJournal) Created() time.Time { return j.manifest.Created }

func (j *exportCheckpointJournal) SourceName() string {
	if j.manifest.Definition.FileName != "" {
		return j.manifest.Definition.FileName
	}
	return workbookDisplayName(j.wb)
}

func writeExportRecoveryJSON(out io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data)+1 > formats.ExportRecoveryMetadata {
		return checkpoint.ErrCapacity
	}
	data = append(data, '\n')
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func readExportRecoveryJSON(in *bufio.Reader, value any) error {
	data, err := in.ReadSlice('\n')
	if err != nil || len(data) > formats.ExportRecoveryMetadata || decodeSavedConfig(data, value) != nil {
		return checkpoint.ErrCorrupt
	}
	return nil
}

func (j *exportCheckpointJournal) Commit(ctx context.Context, part excelExport.ResumePart, produce func(io.Writer) (excelExport.ResumeState, error)) error {
	if part.Before != j.State() || j.manifest.Complete || produce == nil {
		return checkpoint.ErrConflict
	}
	if err := checkExportCheckpointSources(ctx, j.wb, j.manifest.Definition, j.spec, j.selections); err != nil {
		return err
	}
	nextCursor := j.cursor
	m, err := j.lease.AdvanceStream(ctx, j.manifest.Revision, func(out io.Writer) (checkpoint.Advance, error) {
		var next checkpoint.Advance
		if err := writeExportRecoveryJSON(out, part); err != nil {
			return next, err
		}
		body := excelExport.NewPartWriter(out)
		after, err := produce(body)
		if err != nil {
			return next, err
		}
		if err := body.Close(); err != nil {
			return next, err
		}
		if err := excelExport.ValidateResumeTransition(part, after, j.spec.Options, j.selections); err != nil {
			return next, err
		}
		if err := checkExportCheckpointSources(ctx, j.wb, j.manifest.Definition, j.spec, j.selections); err != nil {
			return next, err
		}
		if err := writeExportRecoveryJSON(out, after); err != nil {
			return next, err
		}
		nextCursor.Output = &after
		data, err := json.Marshal(nextCursor)
		stage := "output"
		if after.Phase == "done" {
			stage = "assemble"
		}
		return checkpoint.Advance{Revision: j.manifest.Revision, Stage: stage, Cursor: data, Records: 1}, err
	})
	if err != nil {
		return err
	}
	j.cursor, j.manifest = nextCursor, m
	return nil
}

func (j *exportCheckpointJournal) Visit(ctx context.Context, consume func(excelExport.ResumePart, io.Reader) error) error {
	var visitErr error
	blocks := func(yield func(checkpoint.Block, io.Reader) bool) {
		visitErr = j.lease.VisitCandidate(ctx, func(block checkpoint.Block, source io.Reader) error {
			if !yield(block, source) {
				return context.Canceled
			}
			return nil
		})
	}
	next, stop := iter.Pull2(blocks)
	defer stop()
	var frozen int64
	for n := uint64(0); n < j.cursor.InputBlocks; n++ {
		block, source, ok := next()
		if !ok || block.Records < 1 || block.Bytes != block.Records*4 {
			return errors.Join(checkpoint.ErrCorrupt, visitErr)
		}
		frozen += block.Records
		if _, err := io.Copy(io.Discard, source); err != nil {
			return err
		}
	}
	if frozen != j.cursor.Frozen {
		return checkpoint.ErrCorrupt
	}
	state := excelExport.InitialResumeState(j.spec.Options)
	for n := uint64(0); n < j.State().Commits; n++ {
		block, source, ok := next()
		if !ok {
			return errors.Join(checkpoint.ErrCorrupt, visitErr)
		}
		group := &exportCheckpointPartReader{block: block, source: source, remaining: block.Bytes, next: next}
		in := bufio.NewReaderSize(group, formats.ExportRecoveryMetadata)
		var part excelExport.ResumePart
		if err := readExportRecoveryJSON(in, &part); err != nil || part.Before != state {
			return checkpoint.ErrCorrupt
		}
		body := excelExport.NewPartReader(in)
		if err := consume(part, body); err != nil {
			return err
		}
		var extra [1]byte
		if count, err := body.Read(extra[:]); count != 0 || err != io.EOF {
			return checkpoint.ErrCorrupt
		}
		var after excelExport.ResumeState
		if err := readExportRecoveryJSON(in, &after); err != nil {
			return err
		}
		if err := excelExport.ValidateResumeTransition(part, after, j.spec.Options, j.selections); err != nil {
			return err
		}
		if _, err := in.ReadByte(); err != io.EOF {
			return checkpoint.ErrCorrupt
		}
		state = after
	}
	if _, _, ok := next(); ok {
		return checkpoint.ErrCorrupt
	}
	if visitErr != nil {
		return visitErr
	}
	if state != j.State() {
		return checkpoint.ErrCorrupt
	}
	return ctx.Err()
}

// A logical part can span many physical checkpoint blocks. Only the final
// block has Records=1; no whole-part buffer or temporary copy is created.
type exportCheckpointPartReader struct {
	block     checkpoint.Block
	source    io.Reader
	remaining int64
	next      func() (checkpoint.Block, io.Reader, bool)
}

func (r *exportCheckpointPartReader) Read(out []byte) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}
	if r.block.Records < 0 || r.block.Records > 1 {
		return 0, checkpoint.ErrCorrupt
	}
	if r.remaining == 0 {
		if r.block.Records == 1 {
			return 0, io.EOF
		}
		block, source, ok := r.next()
		if !ok || block.Records > 1 {
			return 0, checkpoint.ErrCorrupt
		}
		r.block, r.source, r.remaining = block, source, block.Bytes
	}
	n, err := r.source.Read(out[:int(min(int64(len(out)), r.remaining))])
	r.remaining -= int64(n)
	if err == io.EOF {
		if r.remaining != 0 {
			return n, io.ErrUnexpectedEOF
		}
		err = nil
	}
	return n, err
}
