package server

import (
	"context"
	"sync"

	"excellens/internal/taskmanager"
)

// Each browser session shares these process-wide capacities. Background
// scans and index builds can use one of the two slots in their respective pool.
type workAdmission struct {
	indexSem, querySem, exportSem chan struct{}
	inspectSem, analysisSem       chan struct{}
	scanQueries, scanIndexes      chan struct{}
}

func newWorkAdmission() workAdmission {
	adm := taskmanager.NewAdmission()
	return workAdmission{
		indexSem:    adm.Index,
		querySem:    adm.Query,
		exportSem:   adm.Export,
		inspectSem:  adm.Inspect,
		analysisSem: adm.Analysis,
		scanQueries: adm.ScanQueries,
		scanIndexes: adm.ScanIndexes,
	}
}

func (work workAdmission) backgroundQuery(ctx context.Context) (func(), error) {
	return taskmanager.AcquireBackground(ctx, work.scanQueries, work.querySem)
}

func (work workAdmission) backgroundIndex(ctx context.Context) (func(), error) {
	return taskmanager.AcquireBackground(ctx, work.scanIndexes, work.indexSem)
}

// Capture and pin the source under the cache read lock. A request that needs
// a query slot releases this lock before queuing, then revalidates its source
// once admitted. A waiting cache writer can block new readers.
func (a *App) lockCacheRead() func() {
	a.cacheMu.RLock()
	return sync.OnceFunc(a.cacheMu.RUnlock)
}

// The caller must not hold cacheMu while waiting for a shared query slot.
func (a *App) acquireQuerySlot(ctx context.Context) (func(), error) {
	return acquireWorkSlot(ctx, a.querySem)
}

func acquireWorkSlot(ctx context.Context, slots chan struct{}) (func(), error) {
	return taskmanager.AcquireSlot(ctx, slots)
}

func (a *App) lockQueryRead(ctx context.Context) (func(), error) {
	releaseSlot, err := a.acquireQuerySlot(ctx)
	if err != nil {
		return nil, err
	}
	a.cacheMu.RLock()
	release := func() {
		a.cacheMu.RUnlock()
		releaseSlot()
	}
	if err = ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
