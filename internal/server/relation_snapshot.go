package server

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"slices"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/reconcile"
	"excellens/internal/taskmanager"
	"excellens/internal/workbook"
)

var errRelationBudgetFull = errors.New("关联结果占用已满，请关闭不用的结果后重试")

type relationInputs = taskmanager.Lease[relationInputData]
type relationSnapshot = taskmanager.Lease[relationSnapshotData]

type relationInputData struct {
	primary *workbook.Workbook
	books   [2]*workbook.Workbook
	refs    [2]reconcileSourceRef
	query   reconcile.RelationQuery
	bytes   int64
}

type relationSnapshotData struct {
	inputs *relationInputs
	plan   *reconcile.Relation
	bytes  int64 // independent of the shared input reservation
}

// Shared across browser sessions. Reservations follow the final independent
// borrower, rather than the UI task entry which originally published a plan.
func (a *App) reserveRelationBytes(bytes int64) (func(), error) {
	return a.rootApp().relationBudget.Reserve(bytes, errRelationBudgetFull)
}

// Caller holds cacheMu.RLock, but not App.mu. Capture and pin both exact book
// objects before queue admission; indexing/source reads occur only in a worker.
func (a *App) pinRelationInputs(primary *workbook.Workbook, refs [2]reconcileSourceRef, query reconcile.RelationQuery) (*relationInputs, error) {
	if primary == nil || len(query.Keys) < 1 || len(query.Keys) > reconcile.MaxKeys || len(query.LeftColumns) > formats.RelationColumns || len(query.RightColumns) > formats.RelationColumns || len(query.LeftColumns)+len(query.RightColumns) == 0 {
		return nil, errors.New("关联来源或字段参数无效")
	}
	input := relationInputData{primary: primary, refs: refs, query: query, bytes: 8192}
	for i, ref := range refs {
		if len(ref.Source) > 512 || len(ref.Stamp) > 128 || ref.HeaderRow < 0 || ref.HeaderRow > formats.ExternalRows || !(strings.HasPrefix(ref.Source, "wb:") && len(ref.Source) > 3 || strings.HasPrefix(ref.Source, "scratch:") && len(ref.Source) > 8) || strings.HasPrefix(ref.Source, "scratch:") && ref.HeaderRow != 0 {
			return nil, errors.New("关联来源或表头参数无效")
		}
		input.books[i] = a.resolveReconcileBook(ref.Book)
		if input.books[i] == nil {
			return nil, errors.New("关联来源已关闭或移除，请重新选择")
		}
		input.bytes += int64(len(ref.Book)+len(ref.Source)+len(ref.Stamp)) * 3
	}
	for _, pair := range query.Keys {
		if len(pair.Mode) > 16 || pair.Left < 1 || pair.Left > formats.ExcelColumns || pair.Right < 1 || pair.Right > formats.ExcelColumns {
			return nil, errors.New("关联键参数无效")
		}
	}
	input.query.Keys = slices.Clone(query.Keys)
	input.query.LeftColumns = slices.Clone(query.LeftColumns)
	input.query.RightColumns = slices.Clone(query.RightColumns)
	releaseBytes, err := a.reserveRelationBytes(input.bytes)
	if err != nil {
		return nil, err
	}
	releasePins := a.pinWorkbooks(primary, input.books[0], input.books[1])
	return taskmanager.NewLease(input, func() { releasePins(); releaseBytes() }), nil
}

func relationSourceHeapBytes(source reconcile.Source) (int64, error) {
	if source.RetainedBytes < 0 || source.RetainedBytes > formats.RelationSnapshotBytes || len(source.Info.Fields) > formats.ExcelColumns {
		return 0, errors.New("关联来源驻留容量无效")
	}
	// Include the original and cloned descriptor slices, allocation classes and
	// independent header strings. File-backed sources retain no whole value table.
	bytes := source.RetainedBytes + 2048 + int64(cap(source.Info.Fields))*256
	info := source.Info
	for _, text := range []string{info.ID, info.Name, info.Sheet, info.Kind, info.Format, info.Notice, info.Fingerprint, info.Stamp} {
		bytes += int64(len(text))*3 + 64
	}
	for _, field := range info.Fields {
		bytes += int64(len(field.Name)+len(field.RawName)+len(field.HeaderHash))*3 + 192
		if bytes > formats.RelationSnapshotBytes {
			return 0, errors.New("关联来源快照超过驻留容量，请减少字段")
		}
	}
	return bytes, nil
}

// Caller already owns one shared query slot and cacheMu.RLock, in that order.
// Preparation retains its input first: cancellation/clearing the queue owner
// cannot release source pins before this worker or a published borrower exits.
func (a *App) prepareRelationSnapshot(ctx context.Context, inputs *relationInputs, progress reconcile.Progress) (*relationSnapshot, error) {
	borrowed, err := inputs.Retain(nil)
	if err != nil {
		return nil, err
	}
	var releases []func()
	var plan *reconcile.Relation
	accepted := false
	defer func() {
		if !accepted {
			if plan != nil {
				if err := plan.Close(); err != nil {
					log.Printf("回收关联行序失败：%v", err)
				}
			}
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			borrowed.Close()
		}
	}()
	input := borrowed.Value()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := input.primary.CheckSourceUnchanged(); err != nil {
		return nil, err
	}
	var sources [2]reconcile.Source
	var retained int64
	charge := func(bytes int64) error {
		if bytes < 0 || bytes > formats.RelationSnapshotBytes-retained {
			return errors.New("关联快照超过驻留容量，请减少结果字段")
		}
		release, err := a.reserveRelationBytes(bytes)
		if err != nil {
			return err
		}
		retained += bytes
		releases = append(releases, release)
		return nil
	}
	for side, book := range input.books {
		sources[side], err = a.buildRelationSource(ctx, book, input.refs[side], reconcile.RelationColumns(input.query, side == 1))
		if err != nil {
			return nil, err
		}
		bytes, err := relationSourceHeapBytes(sources[side])
		if err != nil {
			return nil, err
		}
		if err := charge(bytes); err != nil {
			return nil, err
		}
	}
	plan, err = reconcile.PrepareRelation(ctx, filepath.Join(input.primary.CacheDir, "queries"), input.query, sources[0], sources[1], progress)
	if err != nil {
		return nil, err
	}
	bytes, err := plan.RetainedOrderHeapBytes()
	if err != nil {
		return nil, err
	}
	if err := charge(bytes); err != nil {
		return nil, err
	}
	if err := input.primary.CheckSourceUnchanged(); err != nil {
		return nil, err
	}
	if err := plan.Check(ctx); err != nil {
		return nil, err
	}
	accepted = true
	data := relationSnapshotData{inputs: borrowed, plan: plan, bytes: retained}
	return taskmanager.NewLease(data, func() {
		if err := plan.Close(); err != nil {
			log.Printf("回收关联行序失败：%v", err)
		}
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		borrowed.Close()
	}), nil
}
