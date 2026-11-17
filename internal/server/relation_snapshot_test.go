package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

func relationUsed(a *App) int64 {
	root := a.rootApp()
	root.sourceMu.Lock()
	defer root.sourceMu.Unlock()
	return root.relationBytes
}

func relationPins(a *App) int {
	root := a.rootApp()
	root.sourceMu.Lock()
	defer root.sourceMu.Unlock()
	var count int
	for _, pins := range root.sourcePins {
		count += pins
	}
	return count
}

func relationSnapshotFixture(t *testing.T) (*App, [2]reconcileSourceRef) {
	t.Helper()
	a, _ := recordFixture(t, 12, "")
	wb := a.currentWorkbook()
	id := attachReconcileCSV(t, a, "lookup.csv", "编号,内容\n001,保持右文\nright,右侧独有\n")
	right := a.resolveReconcileBook(id)
	return a, [2]reconcileSourceRef{reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1), reconcileRef(t, a, id, "wb:"+right.Sheets[0].Name, 1)}
}

func relationPinInputs(t *testing.T, a *App, refs [2]reconcileSourceRef, query reconcile.RelationQuery) *relationInputs {
	t.Helper()
	a.cacheMu.RLock()
	inputs, err := a.pinRelationInputs(a.currentWorkbook(), refs, query)
	a.cacheMu.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inputs.close)
	return inputs
}

func relationPrepareSnapshot(a *App, ctx context.Context, inputs *relationInputs, progress reconcile.Progress) (*relationSnapshot, error) {
	select {
	case a.querySem <- struct{}{}:
		defer func() { <-a.querySem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	return a.prepareRelationSnapshot(ctx, inputs, progress)
}

func TestRelationSnapshotPinsBothSourcesBeforePreparationAndSurvivesAllOriginalOwnersClosing(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	beforePins := relationPins(a)
	query := relationServerQuery()
	inputs := relationPinInputs(t, a, refs, query)
	if relationPins(a) != beforePins+3 || relationUsed(a) != inputs.data.value.bytes {
		t.Fatal("queue input did not retain both exact book objects and its budget")
	}
	query.Keys[0].Left, query.LeftColumns[0] = 12, 12
	if inputs.data.value.query.Keys[0].Left != 1 || inputs.data.value.query.LeftColumns[0] != 1 {
		t.Fatal("queued configuration shares a mutable request slice")
	}
	// Attachment removal is allowed once the input owns its independent pins.
	if response := reconcilePost(t, a, "/api/reconcile/source/remove", map[string]string{"book": refs[1].Book}); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	snapshot, err := relationPrepareSnapshot(a, context.Background(), inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.close)
	consumer, err := snapshot.retain()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(consumer.close)
	inputs.close()
	snapshot.close()
	a.Close()
	if relationPins(a) != 3 || relationUsed(a) != consumer.data.value.bytes+consumer.data.value.inputs.data.value.bytes {
		t.Fatal("owner shutdown reclaimed a consumer's resources", relationPins(a), relationUsed(a))
	}
	a.cacheMu.RLock()
	preview, err := consumer.data.value.plan.Preview(context.Background(), "left")
	a.cacheMu.RUnlock()
	if err != nil || len(preview.Rows) != 2 || preview.Rows[0].Left.Values[2].Raw != "9007199254740993.31" || preview.Rows[0].Right.Values[2].Raw != "保持右文" {
		t.Fatal("retained exact source failed after every original owner closed", preview, err)
	}
	consumer.close()
	if relationPins(a) != 0 || relationUsed(a) != 0 {
		t.Fatal("final consumer leaked pins/budget", relationPins(a), relationUsed(a))
	}
}

func TestRelationSnapshotBudgetSharedAcrossTabsAndFailedPreparationRestoresInputReservation(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	inputs := relationPinInputs(t, a, refs, relationServerQuery())
	beforePins, inputBytes := relationPins(a), inputs.data.value.bytes
	child, err := a.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	fill, err := child.app.reserveRelationBytes(formats.RelationRetainedBytes - inputBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fill)
	for _, invalid := range []int64{-1, 1, formats.RelationRetainedBytes + 1} {
		if release, err := a.reserveRelationBytes(invalid); err == nil || release != nil {
			t.Fatal("cross-tab or invalid reservation exceeded shared capacity", invalid)
		}
	}
	failed, err := relationPrepareSnapshot(a, context.Background(), inputs, nil)
	if err == nil || failed != nil || relationPins(a) != beforePins || relationUsed(a) != formats.RelationRetainedBytes {
		t.Fatal("failed preparation lost input ownership or retained extra budget", err, relationPins(a), relationUsed(a))
	}
	fill()
	fill()
	if relationUsed(a) != inputBytes {
		t.Fatal("reservation released more than once", relationUsed(a))
	}
	snapshot, err := relationPrepareSnapshot(a, context.Background(), inputs, nil)
	if err != nil {
		t.Fatal("rejected preparation damaged a valid retry", err)
	}
	snapshot.close()
	inputs.close()
	if relationUsed(a) != 0 {
		t.Fatal("retry retained a detached budget")
	}
}

func TestRelationSnapshotCancelledFailedOrClosedInputsNeverPublishAndReturnEveryBorrow(t *testing.T) {
	for _, kind := range []string{"cancelled", "closed", "stale source", "invalid output", "bad key mode"} {
		t.Run(kind, func(t *testing.T) {
			a, refs := relationSnapshotFixture(t)
			query := relationServerQuery()
			if kind == "stale source" {
				refs[1].Stamp = "stale"
			}
			if kind == "invalid output" {
				query.LeftColumns = []int{999}
			}
			if kind == "bad key mode" {
				query.Keys[0].Mode = "guess"
			}
			inputs := relationPinInputs(t, a, refs, query)
			before := relationPins(a)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			if kind == "closed" {
				inputs.close()
			}
			failed, err := relationPrepareSnapshot(a, ctx, inputs, nil)
			if err == nil || failed != nil || kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatal("invalid input published an apparently complete source", failed, err)
			}
			if kind != "closed" && (inputs.data.refs.Load() != 1 || relationPins(a) != before || relationUsed(a) != inputs.data.value.bytes) {
				t.Fatal("failed worker retained a borrow or budget", relationPins(a), relationUsed(a))
			}
			inputs.close()
			if relationUsed(a) != 0 || workbook.QueryFilesInUse(filepath.Join(a.currentWorkbook().CacheDir, "queries")) {
				t.Fatal("failed preparation left temporary or retained source resources")
			}
		})
	}
}

func TestRelationSnapshotScratchProjectionIsChargedAndSameSizeEditsAreRejected(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	wb := a.currentWorkbook()
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "relation-memory", Name: "分析来源", Columns: []string{"编号", "内容"}, Rows: [][]string{{"001", strings.Repeat("精确", 1000)}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	refs[1] = reconcileRef(t, a, sourceBookID(wb), "scratch:relation-memory", 0)
	inputs := relationPinInputs(t, a, refs, relationServerQuery())
	snapshot, err := relationPrepareSnapshot(a, context.Background(), inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.close)
	if snapshot.data.value.bytes <= int64(len(strings.Repeat("精确", 1000))) {
		t.Fatal("detached scratch source values escaped retained accounting")
	}
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		ws.Sheet("relation-memory").Rows[0][1] = strings.Repeat("修改", 1000)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	a.cacheMu.RLock()
	_, err = snapshot.data.value.plan.Preview(context.Background(), "right_unmatched")
	a.cacheMu.RUnlock()
	if err == nil || !strings.Contains(err.Error(), "已修改") {
		t.Fatal("same row count/byte length edit reused an old source snapshot", err)
	}
	snapshot.close()
	inputs.close()
	if relationUsed(a) != 0 {
		t.Fatal("rejected edited scratch source leaked budget")
	}
}

func TestRelationSnapshotActiveExtractionOutlivesAllItsQueueAndResultOwners(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	inputs := relationPinInputs(t, a, refs, relationServerQuery())
	snapshot, err := relationPrepareSnapshot(a, context.Background(), inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.close)
	consumer, err := snapshot.retain()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(consumer.close)
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(unblock)
	finished := make(chan error, 1)
	go func() {
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		var count int
		_, err := consumer.data.value.plan.Stream(context.Background(), "left", 100, func(row reconcile.RelationRow) error {
			count++
			if count == 1 {
				close(entered)
				<-resume
			}
			return nil
		})
		if err == nil && count != 2 {
			err = errors.New("retained extraction lost source rows")
		}
		consumer.close()
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retained source extraction did not enter")
	}
	inputs.close()
	snapshot.close()
	if relationUsed(a) == 0 || consumer.data.refs.Load() != 1 {
		t.Fatal("active export-like consumer lost the last source lease")
	}
	unblock()
	select {
	case err := <-finished:
		if err != nil || relationUsed(a) != 0 {
			t.Fatal("active consumer did not complete or return its resources", err, relationUsed(a))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retained extraction failed to unwind")
	}
}

func TestRelationSnapshotRejectsChangedUnvisitedFileAndOversizedSourceAccounting(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	inputs := relationPinInputs(t, a, refs, relationServerQuery())
	snapshot, err := relationPrepareSnapshot(a, context.Background(), inputs, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.close)
	right := inputs.data.value.books[1]
	if err := os.WriteFile(right.Path, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	a.cacheMu.RLock()
	err = snapshot.data.value.plan.Check(context.Background())
	a.cacheMu.RUnlock()
	if err == nil {
		t.Fatal("source revalidation omitted an unvisited other file")
	}
	for _, bytes := range []int64{-1, formats.RelationSnapshotBytes + 1} {
		if _, err := relationSourceHeapBytes(reconcile.Source{RetainedBytes: bytes}); err == nil {
			t.Fatal("invalid detached source accounting was accepted", bytes)
		}
	}
}
