package taskmanager

import (
	"context"
	"time"
)

// Lifetime describes a tracked background task's start timestamp, completion
// signal, and cancellation/cleanup hook.
type Lifetime struct {
	Started time.Time
	Done    <-chan struct{}
	Cancel  context.CancelFunc
}

// PruneFinished evicts the oldest completed tasks until len(jobs) < keep.
// Only a closed Done channel proves deferred writes and resource cleanup have
// finished, so exiting tasks whose Done channel is still open are never evicted.
// Returns true if capacity is available (< keep), false if all entries are live.
func PruneFinished[T any](jobs map[string]*T, keep int, lifetime func(*T) Lifetime) bool {
	for len(jobs) >= keep {
		oldestID := ""
		var oldest Lifetime
		for id, job := range jobs {
			state := lifetime(job)
			select {
			case <-state.Done:
				if oldestID == "" || state.Started.Before(oldest.Started) || state.Started.Equal(oldest.Started) && id < oldestID {
					oldestID, oldest = id, state
				}
			default:
			}
		}
		if oldestID == "" {
			return false
		}
		if oldest.Cancel != nil {
			oldest.Cancel()
		}
		delete(jobs, oldestID)
	}
	return true
}

// BudgetLimits bounds retained history count, retained snapshot count, and
// aggregate snapshot bytes for a task registry.
type BudgetLimits struct {
	History   int
	Snapshots int
	Bytes     int64
}

// BudgetEntry describes a task participating in budget-based history pruning.
type BudgetEntry struct {
	Created  time.Time
	Done     <-chan struct{}
	Cancel   context.CancelFunc
	Retained bool
	Bytes    int64
}

// PruneBudgeted evicts the oldest completed tasks (excluding keepID) until the
// map satisfies history, snapshot count, and total byte limits, or no eligible
// completed task remains.
func PruneBudgeted[T any](jobs map[string]*T, keepID string, limits BudgetLimits, inspect func(*T) BudgetEntry) {
	for {
		count, bytes := 0, int64(0)
		oldestID := ""
		var oldest BudgetEntry
		for id, job := range jobs {
			entry := inspect(job)
			if entry.Retained {
				count++
				bytes += entry.Bytes
			}
			select {
			case <-entry.Done:
				if id != keepID && (oldestID == "" || entry.Created.Before(oldest.Created) || entry.Created.Equal(oldest.Created) && id < oldestID) {
					oldestID, oldest = id, entry
				}
			default:
			}
		}
		if len(jobs) <= limits.History && count <= limits.Snapshots && bytes <= limits.Bytes || oldestID == "" {
			return
		}
		if oldest.Cancel != nil {
			oldest.Cancel()
		}
		delete(jobs, oldestID)
	}
}

// ActiveCount counts tasks whose completion channel has not yet been closed.
func ActiveCount[T any](jobs map[string]*T, done func(*T) <-chan struct{}) int {
	active := 0
	for _, job := range jobs {
		select {
		case <-done(job):
		default:
			active++
		}
	}
	return active
}
