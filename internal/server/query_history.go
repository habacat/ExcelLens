package server

import (
	"context"
	"time"
)

type queryTaskLifetime struct {
	started time.Time
	done    <-chan struct{}
	cancel  context.CancelFunc
}

// Caller holds App.mu. Only a closed completion channel proves that deferred
// checkpoint/cache writes and resource release have finished. Never prune a
// displayed terminal task that is still exiting, even when history is full.
func pruneQueryHistory[T any](jobs map[string]*T, keep int, lifetime func(*T) queryTaskLifetime) bool {
	for len(jobs) >= keep {
		oldestID := ""
		var oldest queryTaskLifetime
		for id, job := range jobs {
			state := lifetime(job)
			select {
			case <-state.done:
				if oldestID == "" || state.started.Before(oldest.started) || state.started.Equal(oldest.started) && id < oldestID {
					oldestID, oldest = id, state
				}
			default:
			}
		}
		if oldestID == "" {
			return false
		}
		if oldest.cancel != nil {
			oldest.cancel()
		}
		delete(jobs, oldestID)
	}
	return true
}

func pruneFilterJobsLocked(jobs map[string]*FilterJob, keep int) bool {
	return pruneQueryHistory(jobs, keep, func(job *FilterJob) queryTaskLifetime {
		return queryTaskLifetime{job.Started, job.done, job.cancel}
	})
}

func pruneSearchJobsLocked(jobs map[string]*SearchJob, keep int) bool {
	return pruneQueryHistory(jobs, keep, func(job *SearchJob) queryTaskLifetime {
		return queryTaskLifetime{job.Started, job.done, func() {
			if job.cancel != nil {
				job.cancel()
			}
			job.Results.Close()
		}}
	})
}
