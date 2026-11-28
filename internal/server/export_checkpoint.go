package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"hash"
	"path/filepath"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

const exportCheckpointDriver = "source-export"
const exportCheckpointSemantics = 1

var errExportCaptureIncomplete = i18n.Error("service.exportCheckpoint.exportRangeNotFullySaved")

// No transient filter/view/job ID or downloaded file path enters this payload.
// Implicit physical spans remain constant size, including billion-row blanks.
type exportCheckpointSpec struct {
	Scope         string                  `json:"scope"`
	Options       excelExport.Options     `json:"options"`
	IncludeHidden bool                    `json:"includeHidden"`
	Sheets        []exportCheckpointSheet `json:"sheets"`
}

type exportCheckpointSheet struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	FirstRow   int    `json:"firstRow"`
	LastRow    int    `json:"lastRow"`
	FirstCol   int    `json:"firstCol"`
	LastCol    int    `json:"lastCol"`
	Compact    bool   `json:"compact"`
	Filtered   bool   `json:"filtered"`
	Rows       int    `json:"rows"`
	SourceRows int    `json:"sourceRows"`
	Index      string `json:"index"`
	RowHash    string `json:"rowHash,omitempty"`
}

// Snapshot acknowledges input capture only. It does not mark output complete.
// Later format drivers extend the output cursor independently of Frozen.
type exportTaskCursor struct {
	Snapshot    bool                     `json:"snapshot"`
	Frozen      int64                    `json:"frozen,string"`
	InputBlocks uint64                   `json:"inputBlocks,string,omitempty"`
	Output      *excelExport.ResumeState `json:"output,omitempty"`
}

func exportTaskSource(wb *workbook.Workbook) (checkpoint.Source, error) {
	if wb == nil {
		return checkpoint.Source{}, checkpoint.ErrBinding
	}
	data, err := json.Marshal([]any{exportCheckpointSemantics, wb.Fingerprint, wb.Format, wb.Date1904, wb.SourceInfo, wb.Sheets})
	if err != nil {
		return checkpoint.Source{}, err
	}
	stamp := sha256.Sum256(data)
	return checkpointWorkbookSource(wb, "wb:export", hex.EncodeToString(stamp[:]))
}

func exportTaskDefinition(ctx context.Context, wb *workbook.Workbook, req exportRequest, selections []excelExport.Selection) (checkpoint.Definition, error) {
	var empty checkpoint.Definition
	if wb == nil {
		return empty, checkpoint.ErrBinding
	}
	if err := excelExport.Validate(req.options(), selections); err != nil {
		return empty, err
	}
	spec := exportCheckpointSpec{Scope: req.Scope, Options: req.options(), IncludeHidden: req.IncludeHidden}
	for _, s := range selections {
		stamp, err := wb.CheckpointIndexStamp(ctx, s.Sheet, s.Index, true)
		if err != nil {
			return empty, err
		}
		sheet := exportCheckpointSheet{Name: s.Sheet.Name, Path: s.Sheet.Path, FirstRow: s.FirstRow, LastRow: s.LastRow, FirstCol: s.FirstCol, LastCol: s.LastCol, Compact: s.Compact, Filtered: s.Filtered, Rows: int(s.RowCount()), SourceRows: s.Index.RowCount(), Index: stamp}
		if s.Filtered {
			digest := sha256.New()
			if err := visitExportSelectionNumbers(ctx, s, func(number int) error { return hashExportRow(digest, number) }); err != nil {
				return empty, err
			}
			sheet.RowHash = hex.EncodeToString(digest.Sum(nil))
		}
		spec.Sheets = append(spec.Sheets, sheet)
	}
	source, err := exportTaskSource(wb)
	if err != nil {
		return empty, err
	}
	payload, err := json.Marshal(spec)
	if err != nil {
		return empty, err
	}
	if len(payload) > formats.CheckpointDefinitionBytes {
		return empty, checkpoint.ErrCapacity
	}
	def := checkpoint.Definition{Kind: "export", Driver: exportCheckpointDriver, Semantics: exportCheckpointSemantics, Title: checkpointTitle("导出", source.Path), FileName: workbookDisplayName(wb), Sources: []checkpoint.Source{source}, Payload: payload}
	if err := setTaskDigest(ctx, wb, &def); err != nil {
		return empty, err
	}
	if _, err := exportCheckpointSpecFor(checkpoint.Manifest{Definition: def}); err != nil {
		return empty, err
	}
	if err := checkExportCheckpointSources(ctx, wb, def, spec, selections); err != nil {
		return empty, err
	}
	return def, nil
}

func createExportCheckpoint(ctx context.Context, store *checkpoint.Store, wb *workbook.Workbook, req exportRequest, selections []excelExport.Selection) (*checkpoint.Lease, error) {
	def, err := exportTaskDefinition(ctx, wb, req, selections)
	if err != nil {
		return nil, err
	}
	cursor, _ := json.Marshal(exportTaskCursor{})
	return retryCheckpointBusy(ctx, func() (*checkpoint.Lease, error) { return store.Create(ctx, def, "capture", cursor) })
}

func validExportDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && hex.EncodeToString(data) == value
}

func exportCheckpointSpecFor(m checkpoint.Manifest) (exportCheckpointSpec, error) {
	var spec exportCheckpointSpec
	d := m.Definition
	if d.Kind != "export" || d.Driver != exportCheckpointDriver || d.Semantics != exportCheckpointSemantics || len(d.Sources) != 1 || decodeSavedConfig(d.Payload, &spec) != nil || excelExport.ValidateOptions(spec.Options) != nil || len(spec.Sheets) == 0 || len(spec.Sheets) > 256 {
		return spec, checkpoint.ErrBinding
	}
	format, found := formats.Lookup(d.Sources[0].Format)
	if !found {
		return spec, checkpoint.ErrBinding
	}
	switch spec.Scope {
	case "selection", "sheet", "filtered", "visible":
		if len(spec.Sheets) != 1 {
			return spec, checkpoint.ErrBinding
		}
	case "all":
		if spec.Options.Format != "xlsx" && spec.Options.Format != "zipcsv" {
			return spec, checkpoint.ErrBinding
		}
	default:
		return spec, checkpoint.ErrBinding
	}
	names, paths := make(map[string]bool), make(map[string]bool)
	var frozen int64
	for _, s := range spec.Sheets {
		if s.Name == "" || s.Path == "" || names[s.Name] || paths[s.Path] || s.FirstRow < 1 || s.LastRow < s.FirstRow || s.LastRow > format.MaxRows || s.FirstCol < 1 || s.LastCol < s.FirstCol || s.LastCol > formats.ExcelColumns || s.Rows < 0 || s.Rows > format.MaxRows || s.SourceRows < 0 || s.SourceRows > format.MaxRows || !validExportDigest(s.Index) {
			return spec, checkpoint.ErrBinding
		}
		names[s.Name], paths[s.Path] = true, true
		if s.Filtered {
			if !s.Compact || !validExportDigest(s.RowHash) || spec.Scope == "sheet" || spec.Scope == "all" {
				return spec, checkpoint.ErrBinding
			}
			frozen += int64(s.Rows)
		} else if s.RowHash != "" || s.Rows != s.LastRow-s.FirstRow+1 || spec.Scope == "filtered" {
			return spec, checkpoint.ErrBinding
		}
		if (spec.Scope == "sheet" || spec.Scope == "all") && (s.Compact || s.FirstRow != 1 || s.FirstCol != 1) || (spec.Scope == "selection" || spec.Scope == "visible") && !s.Compact {
			return spec, checkpoint.ErrBinding
		}
	}
	if frozen > formats.CheckpointBytes/4 {
		return spec, checkpoint.ErrCapacity
	}
	return spec, nil
}

func exportCheckpointCursor(m checkpoint.Manifest, spec exportCheckpointSpec) (exportTaskCursor, error) {
	var cursor exportTaskCursor
	var total int64
	for _, sheet := range spec.Sheets {
		if sheet.Filtered {
			total += int64(sheet.Rows)
		}
	}
	if decodeSavedConfig(m.Cursor, &cursor) != nil || cursor.Frozen < 0 || cursor.Frozen > total || cursor.InputBlocks > m.Blocks {
		return cursor, checkpoint.ErrCorrupt
	}
	if cursor.Output != nil {
		shape := make([]excelExport.ResumeSheet, len(spec.Sheets))
		for i, sheet := range spec.Sheets {
			shape[i] = excelExport.ResumeSheet{Rows: int64(sheet.Rows), Columns: sheet.LastCol - sheet.FirstCol + 1}
		}
		if !cursor.Snapshot || cursor.Frozen != total || cursor.Frozen > 0 && cursor.InputBlocks == 0 || excelExport.ValidateResumeShape(*cursor.Output, spec.Options, shape) != nil || cursor.Output.Commits == 0 || cursor.Output.Commits > uint64(m.Records) || m.Records-int64(cursor.Output.Commits) != cursor.Frozen || m.Blocks-cursor.InputBlocks < cursor.Output.Commits {
			return cursor, checkpoint.ErrCorrupt
		}
		stage := "output"
		if cursor.Output.Phase == "done" {
			stage = "assemble"
			if m.Complete {
				stage = "done"
			}
		} else if m.Complete {
			return cursor, checkpoint.ErrCorrupt
		}
		if m.Stage != stage {
			return cursor, checkpoint.ErrCorrupt
		}
		return cursor, nil
	}
	if cursor.Frozen != m.Records || m.Complete || m.Blocks > uint64(cursor.Frozen) {
		return cursor, checkpoint.ErrCorrupt
	}
	if cursor.Snapshot {
		if cursor.Frozen != total || m.Stage != "ready" || cursor.InputBlocks != 0 && cursor.InputBlocks != m.Blocks {
			return cursor, checkpoint.ErrCorrupt
		}
	} else if m.Stage != "capture" || cursor.InputBlocks != 0 {
		return cursor, checkpoint.ErrCorrupt
	}
	return cursor, nil
}

func checkExportCheckpointSources(ctx context.Context, wb *workbook.Workbook, def checkpoint.Definition, spec exportCheckpointSpec, selections []excelExport.Selection) error {
	return checkExportSources(ctx, wb, def, spec, selections, taskIndexes{portable: len(def.Sources) == 1 && def.Sources[0].SHA256 != ""})
}
func checkExportSources(ctx context.Context, wb *workbook.Workbook, def checkpoint.Definition, spec exportCheckpointSpec, selections []excelExport.Selection, policy taskIndexes) error {
	source, err := exportTaskSource(wb)
	if err != nil {
		return err
	}
	if len(def.Sources) != 1 || len(selections) != len(spec.Sheets) {
		return checkpoint.ErrBinding
	}
	if err := bindTaskSource(ctx, wb, source, def.Sources[0], true); err != nil {
		return err
	}
	for i, s := range selections {
		bound := spec.Sheets[i]
		stamp, err := wb.CheckpointIndexStamp(ctx, s.Sheet, s.Index, policy.portable)
		if err != nil {
			return err
		}
		if s.Sheet.Name != bound.Name || s.Sheet.Path != bound.Path || s.Index.RowCount() != bound.SourceRows || stamp != policy.expected(bound.Index) {
			return checkpoint.ErrBinding
		}
	}
	return ctx.Err()
}

func hashExportRow(digest hash.Hash, number int) error {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], uint32(number))
	_, err := digest.Write(raw[:])
	return err
}

// Only explicit row sequences need durable blocks; physical spans are already
// described by the immutable definition. False blanks/positions are rejected
// before a caller can turn an invalid reference into an innocent row number.
func visitExportSelectionNumbers(ctx context.Context, s excelExport.Selection, visit func(int) error) error {
	seen := int64(0)
	add := func(number int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen >= s.RowCount() || number < 1 || number > workbook.MaxRowsForIndex(s.Index) {
			return checkpoint.ErrBinding
		}
		seen++
		return visit(number)
	}
	switch {
	case s.Rows != nil:
		for ref := range s.Rows.Rows() {
			if ref.Position < -1 || ref.Position >= s.Index.RowCount() || ref.Position >= 0 && s.Index.RowAt(ref.Position).Number != ref.Number {
				return checkpoint.ErrBinding
			}
			if ref.Position == -1 {
				position := s.Index.FindRowOffset(ref.Number)
				if position < s.Index.RowCount() && s.Index.RowAt(position).Number == ref.Number {
					return checkpoint.ErrBinding
				}
			}
			if err := add(ref.Number); err != nil {
				return err
			}
		}
	case s.RowNumbers != nil:
		for _, number := range s.RowNumbers {
			if err := add(number); err != nil {
				return err
			}
		}
	case s.Filtered:
		for position := range s.Order.Iter() {
			if err := add(s.Index.RowAt(position).Number); err != nil {
				return err
			}
		}
	default:
		return checkpoint.ErrBinding
	}
	if seen != s.RowCount() {
		return checkpoint.ErrBinding
	}
	return ctx.Err()
}

// captureExportCheckpoint is an input stage, deliberately separate from the
// future record/part writer. An interrupted capture cannot recover temporary
// view semantics; it stays explicitly incomplete rather than rescanning.
func captureExportCheckpoint(ctx context.Context, wb *workbook.Workbook, lease *checkpoint.Lease, selections []excelExport.Selection) error {
	m := lease.Snapshot()
	spec, err := exportCheckpointSpecFor(m)
	if err != nil {
		return err
	}
	cursor, err := exportCheckpointCursor(m, spec)
	if err != nil {
		return err
	}
	if cursor.Snapshot || cursor.Frozen != 0 || m.Blocks != 0 {
		return checkpoint.ErrConflict
	}
	if err := checkExportCheckpointSources(ctx, wb, m.Definition, spec, selections); err != nil {
		return err
	}
	buffer := make([]byte, 0, formats.CheckpointOrderRows*4)
	flush := func() error {
		if len(buffer) == 0 {
			return nil
		}
		cursor.Frozen += int64(len(buffer) / 4)
		data, _ := json.Marshal(cursor)
		var err error
		m, err = advanceTaskCheckpoint(ctx, lease, checkpoint.Advance{Revision: m.Revision, Stage: "capture", Cursor: data, Records: int64(len(buffer) / 4), Bytes: int64(len(buffer))}, buffer)
		buffer = buffer[:0]
		return err
	}
	for i, s := range selections {
		bound := spec.Sheets[i]
		if s.FirstRow != bound.FirstRow || s.LastRow != bound.LastRow || s.FirstCol != bound.FirstCol || s.LastCol != bound.LastCol || s.Filtered != bound.Filtered || s.Compact != bound.Compact || s.RowCount() != int64(bound.Rows) {
			return checkpoint.ErrBinding
		}
		if !s.Filtered {
			continue
		}
		digest := sha256.New()
		candidate, err := workbook.BuildSourceRowSnapshot(ctx, filepath.Join(wb.CacheDir, "queries"), s.Index, bound.Rows, func(ctx context.Context, add func(int) error) error {
			return visitExportSelectionNumbers(ctx, s, func(number int) error {
				if err := add(number); err != nil {
					return err
				}
				buffer = binary.LittleEndian.AppendUint32(buffer, uint32(number))
				_ = hashExportRow(digest, number)
				if len(buffer) == cap(buffer) {
					return flush()
				}
				return nil
			})
		})
		if err != nil {
			return err
		}
		if err := candidate.Close(); err != nil {
			return err
		}
		if hex.EncodeToString(digest.Sum(nil)) != bound.RowHash {
			return checkpoint.ErrBinding
		}
		if err := flush(); err != nil {
			return err
		}
	}
	if err := checkExportCheckpointSources(ctx, wb, m.Definition, spec, selections); err != nil {
		return err
	}
	cursor.Snapshot, cursor.InputBlocks = true, m.Blocks
	data, _ := json.Marshal(cursor)
	_, err = advanceTaskCheckpoint(ctx, lease, checkpoint.Advance{Revision: m.Revision, Stage: "ready", Cursor: data}, nil)
	return err
}
