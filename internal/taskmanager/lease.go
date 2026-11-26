package taskmanager

import (
	"errors"
	"runtime"
	"sync/atomic"
)

// ErrLeaseReleased is returned when Retain is called on a closed lease without
// a caller-supplied error message.
var ErrLeaseReleased = errors.New("来源快照已释放，请重新运行")

// Lease owns one independent reference to an immutable shared resource.
// Retain before handing a snapshot to another task; closing one lease never
// cancels another. Resource teardown runs exactly once, outside caller locks.
type Lease[T any] struct {
	data   *leaseData[T]
	closed atomic.Bool
}

type leaseData[T any] struct {
	refs      atomic.Int64
	value     atomic.Pointer[T]
	release   func()
	finalized bool
}

// NewLease creates an owned reference to value whose release callback runs
// once when the final lease handle is closed.
func NewLease[T any](value T, release func()) *Lease[T] {
	data := &leaseData[T]{release: release}
	data.value.Store(&value)
	data.refs.Store(1)
	return &Lease[T]{data: data}
}

// NewFinalizedLease behaves like NewLease and also attaches a GC finalizer as
// a safety net in case an abandoned handle is dropped without Close.
func NewFinalizedLease[T any](value T, release func()) *Lease[T] {
	data := &leaseData[T]{release: release, finalized: true}
	data.value.Store(&value)
	data.refs.Store(1)
	l := &Lease[T]{data: data}
	runtime.SetFinalizer(l, (*Lease[T]).Close)
	return l
}

// Value returns the leased value. The caller must retain an open lease while
// using the returned value.
func (l *Lease[T]) Value() T {
	if l != nil && l.data != nil && !l.closed.Load() {
		defer runtime.KeepAlive(l)
		if v := l.data.value.Load(); v != nil {
			return *v
		}
	}
	var zero T
	return zero
}

// Refs returns the current reference count of the underlying shared resource.
func (l *Lease[T]) Refs() int64 {
	if l == nil || l.data == nil {
		return 0
	}
	return l.data.refs.Load()
}

// Closed reports whether this handle has already been closed.
func (l *Lease[T]) Closed() bool {
	return l == nil || l.closed.Load()
}

// Same reports whether l and other reference the same underlying shared lease.
func (l *Lease[T]) Same(other *Lease[T]) bool {
	return l != nil && other != nil && l.data != nil && l.data == other.data
}

// Retain acquires an independent lease sharing the same underlying value.
// If this handle is already closed or the final reference has been released,
// Retain returns errClosed (or ErrLeaseReleased if errClosed is nil).
func (l *Lease[T]) Retain(errClosed error) (*Lease[T], error) {
	if errClosed == nil {
		errClosed = ErrLeaseReleased
	}
	if l == nil || l.data == nil || l.closed.Load() {
		return nil, errClosed
	}
	defer runtime.KeepAlive(l)
	for {
		n := l.data.refs.Load()
		if n <= 0 || l.closed.Load() {
			return nil, errClosed
		}
		if l.data.refs.CompareAndSwap(n, n+1) {
			retained := &Lease[T]{data: l.data}
			if l.data.finalized {
				runtime.SetFinalizer(retained, (*Lease[T]).Close)
			}
			if l.closed.Load() {
				retained.Close()
				return nil, errClosed
			}
			return retained, nil
		}
	}
}

// Close releases this lease handle. Close is idempotent; when the last handle
// closes, the release callback runs once and the underlying payload is cleared.
func (l *Lease[T]) Close() {
	if l == nil || l.data == nil || !l.closed.CompareAndSwap(false, true) {
		return
	}
	if l.data.finalized {
		runtime.SetFinalizer(l, nil)
	}
	if l.data.refs.Add(-1) != 0 {
		return
	}
	release := l.data.release
	l.data.release = nil
	if release != nil {
		release()
	}
	// Closed handles may remain in a caller's cancellation/history state. They
	// must not keep detached source values alive after returning their budget.
	l.data.value.Store(nil)
}
