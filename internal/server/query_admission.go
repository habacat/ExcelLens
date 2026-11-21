package server

import (
	"context"
	"sync"
)

// Each browser session shares these process-wide capacities. Background
// scans and index builds can use one of the two slots in their respective pool.
type workAdmission struct {
	indexSem, querySem, exportSem chan struct{}
	inspectSem, analysisSem       chan struct{}
	scanQueries, scanIndexes      chan struct{}
}

func newWorkAdmission() workAdmission {
	return workAdmission{
		indexSem: make(chan struct{}, 2), querySem: make(chan struct{}, 2), exportSem: make(chan struct{}, 2),
		inspectSem: make(chan struct{}, 1), analysisSem: make(chan struct{}, 1),
		scanQueries: make(chan struct{}, 1), scanIndexes: make(chan struct{}, 1),
	}
}

func (work workAdmission) backgroundQuery(ctx context.Context) (func(), error) {
	return acquireBackground(ctx, work.scanQueries, work.querySem)
}

func (work workAdmission) backgroundIndex(ctx context.Context) (func(), error) {
	return acquireBackground(ctx, work.scanIndexes, work.indexSem)
}

func acquireBackground(ctx context.Context, background, total chan struct{}) (func(), error) {
	releaseBudget, err := acquireWorkSlot(ctx, background)
	if err != nil {
		return nil, err
	}
	releaseSlot, err := acquireWorkSlot(ctx, total)
	if err != nil {
		releaseBudget()
		return nil, err
	}
	return func() { releaseSlot(); releaseBudget() }, nil
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if slots == nil {
		return func() {}, nil
	}
	select {
	case slots <- struct{}{}:
		return sync.OnceFunc(func() { <-slots }), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
