package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"iter"
	"testing"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

type exportCheckpointTestRows struct {
	index *workbook.SheetIndex
	count int
	refs  iter.Seq[workbook.SourceRowReference]
}

func (s exportCheckpointTestRows) Index() *workbook.SheetIndex { return s.index }
func (s exportCheckpointTestRows) Len() int                    { return s.count }
func (s exportCheckpointTestRows) Rows() iter.Seq[workbook.SourceRowReference] {
	return s.refs
}

func TestExportCheckpointRejectsFalseBlankWrongPositionAndUnstableCapture(t *testing.T) {
	for _, bad := range []string{"false-blank", "wrong-position", "short", "long", "duplicate", "changed-order"} {
		t.Run(bad, func(t *testing.T) {
			isolateTestStorage(t)
			a, _ := rowViewFixture(t)
			wb := a.currentWorkbook()
			meta, _ := wb.FindSheet("First")
			idx := wb.GetCachedIndex(meta.Path)
			req := exportRequest{Scope: "filtered", Sheet: meta.Name, Format: "csv", Mode: "values"}
			refs := []workbook.SourceRowReference{{Number: 1, Position: 0}, {Number: 4, Position: 3}}
			rows := exportCheckpointTestRows{index: idx, count: 2, refs: func(yield func(workbook.SourceRowReference) bool) {
				for _, ref := range refs {
					if !yield(ref) {
						return
					}
				}
			}}
			s := excelExport.Selection{Sheet: meta, Index: idx, FirstRow: 1, LastRow: 7, FirstCol: 1, LastCol: 2, Filtered: true, Compact: true, Rows: rows}
			if bad == "changed-order" || bad == "duplicate" {
				_, lease := exportCheckpointCreate(t, a, req, []excelExport.Selection{s})
				if bad == "changed-order" {
					refs[0], refs[1] = refs[1], refs[0]
				} else {
					refs[1] = refs[0]
				}
				if err := captureExportCheckpoint(context.Background(), wb, lease, []excelExport.Selection{s}); err == nil || lease.Snapshot().Stage == "ready" {
					t.Fatal("changed/duplicate row order became a confirmed snapshot", err)
				}
				return
			}
			switch bad {
			case "false-blank":
				refs[0].Position = -1
			case "wrong-position":
				refs[0].Position = 1
			case "short":
				refs = refs[:1]
			case "long":
				refs = append(refs, workbook.SourceRowReference{Number: 6, Position: -1})
			}
			if _, err := exportTaskDefinition(context.Background(), wb, req, []excelExport.Selection{s}); err == nil {
				t.Fatal("invalid source references entered an immutable definition", bad)
			}
		})
	}
}

func TestExportCheckpointVerifiedContainerStillRejectsWrongRowShapeAndDigest(t *testing.T) {
	for _, bad := range []string{"duplicate", "bounds", "hash", "short-block"} {
		t.Run(bad, func(t *testing.T) {
			isolateTestStorage(t)
			a, _ := rowViewFixture(t)
			wb := a.currentWorkbook()
			meta, _ := wb.FindSheet("First")
			idx := wb.GetCachedIndex(meta.Path)
			s := excelExport.Selection{Sheet: meta, Index: idx, FirstRow: 1, LastRow: 7, FirstCol: 1, LastCol: 2, Filtered: true, Compact: true, RowNumbers: []int{1, 4, 6}}
			req := exportRequest{Scope: "filtered", Sheet: meta.Name, Format: "csv", Mode: "values"}
			_, lease := exportCheckpointCreate(t, a, req, []excelExport.Selection{s})
			numbers := []int{1, 4, 6}
			switch bad {
			case "duplicate":
				numbers[2] = 4
			case "bounds":
				numbers[2] = workbook.MaxRowsForIndex(idx) + 1
			case "hash":
				numbers[0], numbers[1] = numbers[1], numbers[0]
			}
			var payload []byte
			for _, number := range numbers {
				payload = binary.LittleEndian.AppendUint32(payload, uint32(number))
			}
			if bad == "short-block" {
				payload = payload[:len(payload)-1]
			}
			cursor, _ := json.Marshal(exportTaskCursor{Snapshot: true, Frozen: 3})
			m := lease.Snapshot()
			if _, err := advanceTaskCheckpoint(context.Background(), lease, checkpoint.Advance{Revision: m.Revision, Stage: "ready", Cursor: cursor, Records: 3, Bytes: int64(len(payload))}, payload); err != nil {
				t.Fatal(err)
			}
			if err := lease.Verify(context.Background()); err != nil {
				t.Fatal("fixture should be a fully checksummed container", err)
			}
			if out, err := readExportCheckpointSelections(context.Background(), wb, lease); out != nil || err == nil {
				t.Fatal("bad shape/digest published a private candidate", bad, out, err)
			}
			// A failed suspended pull iterator must release the lease lock.
			if lease.Snapshot().Stage != "ready" {
				t.Fatal("input rejection mutated the durable stage")
			}
		})
	}
}

func TestExportCheckpointCancelledReadKeepsConfirmedInputRevision(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	req := exportRequest{Scope: "sheet", Sheet: "First", Format: "csv", Mode: "values"}
	selections := exportCheckpointPrepare(t, a, req)
	_, lease := exportCheckpointCreate(t, a, req, selections)
	if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	m := lease.Snapshot()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := readExportCheckpointSelections(ctx, wb, lease); out != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled recovery published input", out, err)
	}
	if lease.Snapshot().Revision != m.Revision {
		t.Fatal("reading inputs advanced the durable task")
	}
}
