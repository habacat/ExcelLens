package server

import "excellens/internal/taskmanager"

type queryTaskLifetime = taskmanager.Lifetime

// Caller holds App.mu. Only a closed completion channel proves that deferred
// checkpoint/cache writes and resource release have finished. Never prune a
// displayed terminal task that is still exiting, even when history is full.
func pruneQueryHistory[T any](jobs map[string]*T, keep int, lifetime func(*T) queryTaskLifetime) bool {
	return taskmanager.PruneFinished(jobs, keep, lifetime)
}

func pruneFilterJobsLocked(jobs map[string]*FilterJob, keep int) bool {
	return pruneQueryHistory(jobs, keep, func(job *FilterJob) queryTaskLifetime {
		return queryTaskLifetime{Started: job.Started, Done: job.done, Cancel: job.cancel}
	})
}

func pruneSearchJobsLocked(jobs map[string]*SearchJob, keep int) bool {
	return pruneQueryHistory(jobs, keep, func(job *SearchJob) queryTaskLifetime {
		return queryTaskLifetime{Started: job.Started, Done: job.done, Cancel: func() {
			if job.cancel != nil {
				job.cancel()
			}
			job.Results.Close()
		}}
	})
}
