package server

import (
	"bytes"
	"context"
	"errors"
	"time"

	"excellens/internal/checkpoint"
)

// Directory admission locks are deliberately nonblocking. Short contention
// with a metadata listing or another task must not fail a long source scan.
// Busy means admission has not changed persistent state; uncertain writes and
// every other failure are returned immediately, never replayed speculatively.
func retryCheckpointBusy[T any](ctx context.Context, attempt func() (T, error)) (T, error) {
	deadline := time.Now().Add(time.Second)
	for {
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
		value, err := attempt()
		if !errors.Is(err, checkpoint.ErrBusy) || errors.Is(err, checkpoint.ErrUncertain) || !time.Now().Before(deadline) {
			return value, err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			var zero T
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}

func advanceSearchCheckpoint(ctx context.Context, lease *checkpoint.Lease, next checkpoint.Advance, payload []byte) (checkpoint.Manifest, error) {
	return advanceTaskCheckpoint(ctx, lease, next, payload)
}

func advanceTaskCheckpoint(ctx context.Context, lease *checkpoint.Lease, next checkpoint.Advance, payload []byte) (checkpoint.Manifest, error) {
	return retryCheckpointBusy(ctx, func() (checkpoint.Manifest, error) {
		return lease.Advance(ctx, next, bytes.NewReader(payload))
	})
}

func listTaskCheckpoints(ctx context.Context, store *checkpoint.Store) ([]checkpoint.Entry, error) {
	return retryCheckpointBusy(ctx, store.List)
}
