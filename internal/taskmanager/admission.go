package taskmanager

import (
	"context"
	"sync"
)

// Admission holds the process-wide bounded concurrency pools shared across
// browser sessions. Background scans and index builds can occupy at most one
// slot from their respective pool so interactive queries stay responsive.
type Admission struct {
	Index       chan struct{}
	Query       chan struct{}
	Export      chan struct{}
	Inspect     chan struct{}
	Analysis    chan struct{}
	ScanQueries chan struct{}
	ScanIndexes chan struct{}
}

// NewAdmission creates the default process-wide concurrency pools.
func NewAdmission() Admission {
	return Admission{
		Index:       make(chan struct{}, 2),
		Query:       make(chan struct{}, 2),
		Export:      make(chan struct{}, 2),
		Inspect:     make(chan struct{}, 1),
		Analysis:    make(chan struct{}, 1),
		ScanQueries: make(chan struct{}, 1),
		ScanIndexes: make(chan struct{}, 1),
	}
}

// AcquireSlot reserves one capacity token from slots until the returned
// release callback is invoked, or returns ctx.Err() if cancelled while waiting.
func AcquireSlot(ctx context.Context, slots chan struct{}) (func(), error) {
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

// AcquireBackground acquires a background budget token before acquiring a slot
// from total, releasing in reverse order on cancellation or completion.
func AcquireBackground(ctx context.Context, background, total chan struct{}) (func(), error) {
	releaseBudget, err := AcquireSlot(ctx, background)
	if err != nil {
		return nil, err
	}
	releaseSlot, err := AcquireSlot(ctx, total)
	if err != nil {
		releaseBudget()
		return nil, err
	}
	return func() {
		releaseSlot()
		releaseBudget()
	}, nil
}

func (a Admission) BackgroundQuery(ctx context.Context) (func(), error) {
	return AcquireBackground(ctx, a.ScanQueries, a.Query)
}

func (a Admission) BackgroundIndex(ctx context.Context) (func(), error) {
	return AcquireBackground(ctx, a.ScanIndexes, a.Index)
}
