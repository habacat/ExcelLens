package server

import (
	"errors"
	"sync/atomic"
)

// A lease owns one reference to immutable source resources. Retain before
// handing a snapshot to another task; closing one lease never cancels another.
// Resource teardown runs exactly once, outside application state locks.
type sourceLease[T any] struct {
	data   *sourceLeaseData[T]
	closed atomic.Bool
}

type sourceLeaseData[T any] struct {
	refs    atomic.Int64
	value   T
	release func()
}

func newSourceLease[T any](value T, release func()) *sourceLease[T] {
	data := &sourceLeaseData[T]{value: value, release: release}
	data.refs.Store(1)
	return &sourceLease[T]{data: data}
}

func (s *sourceLease[T]) retain() (*sourceLease[T], error) {
	if s == nil || s.closed.Load() {
		return nil, errors.New("来源快照已释放，请重新运行")
	}
	for {
		n := s.data.refs.Load()
		if n <= 0 || s.closed.Load() {
			return nil, errors.New("来源快照已释放，请重新运行")
		}
		if s.data.refs.CompareAndSwap(n, n+1) {
			retained := &sourceLease[T]{data: s.data}
			if s.closed.Load() {
				retained.close()
				return nil, errors.New("来源快照已释放，请重新运行")
			}
			return retained, nil
		}
	}
}

func (s *sourceLease[T]) close() {
	if s == nil || !s.closed.CompareAndSwap(false, true) || s.data.refs.Add(-1) != 0 {
		return
	}
	release := s.data.release
	s.data.release = nil
	if release != nil {
		release()
	}
	// Closed handles may remain in a caller's cancellation/history state. They
	// must not keep detached source values alive after returning their budget.
	var zero T
	s.data.value = zero
}
