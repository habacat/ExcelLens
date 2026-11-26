package taskmanager

import (
	"errors"
	"sync"
)

// ErrBudgetExceeded is returned when Reserve exceeds the configured byte limit
// and no caller-specific error is provided.
var ErrBudgetExceeded = errors.New("内存占用已满，请关闭不用的结果或任务后重试")

// ByteBudget accounts for bounded retained byte reservations shared across
// sessions and tasks. Each successful Reserve returns an idempotent release
// callback that returns the exact reserved bytes.
type ByteBudget struct {
	mu    sync.Mutex
	limit int64
	used  int64
}

// NewByteBudget creates a byte budget capped at limit bytes.
func NewByteBudget(limit int64) *ByteBudget {
	return &ByteBudget{limit: limit}
}

// Reserve attempts to charge bytes against the budget. It returns an
// idempotent release closure on success, or fullErr if bytes is negative or
// would exceed the budget limit.
func (b *ByteBudget) Reserve(bytes int64, fullErr error) (func(), error) {
	if fullErr == nil {
		fullErr = ErrBudgetExceeded
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes < 0 || bytes > b.limit-b.used {
		return nil, fullErr
	}
	b.used += bytes
	return sync.OnceFunc(func() {
		b.mu.Lock()
		b.used -= bytes
		b.mu.Unlock()
	}), nil
}

// Used returns the currently reserved byte count.
func (b *ByteBudget) Used() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Limit returns the maximum capacity of this budget.
func (b *ByteBudget) Limit() int64 {
	return b.limit
}
