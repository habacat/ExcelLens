package server

import (
	"context"
	"sync"
)

// Capture and pin the source under the cache read lock. A request that needs
// a query slot releases this lock before queuing, then revalidates its source
// once admitted. A waiting cache writer can block new readers.
func (a *App) lockCacheRead() func() {
	a.cacheMu.RLock()
	return sync.OnceFunc(a.cacheMu.RUnlock)
}

// The caller must not hold cacheMu while waiting for a shared query slot.
func (a *App) acquireQuerySlot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slots := a.querySem
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
