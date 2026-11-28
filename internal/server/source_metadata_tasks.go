package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"excellens/internal/i18n"
	"excellens/internal/taskmanager"
	"excellens/internal/workbook"
)

// App.mu protects task state and its map. Published snapshots are immutable;
// the workbook and prepared index are captured before entering the query queue.
type sourceMetadataTask[T any] struct {
	ID, Mode, Status, Error, Stage string
	Workbook                       *workbook.Workbook
	Meta                           workbook.SheetMeta
	Index                          *workbook.SheetIndex
	DoneBytes, TotalBytes          int64
	Created                        time.Time
	Snapshot                       *T
	discarded                      bool
	ctx                            context.Context
	cancel                         context.CancelFunc
	done                           chan struct{}
}

type metadataTaskLimits struct {
	history, snapshots int
	bytes              int64
}

func activeMetadataTasks[T any](jobs map[string]*sourceMetadataTask[T]) int {
	return taskmanager.ActiveCount(jobs, func(job *sourceMetadataTask[T]) <-chan struct{} {
		return job.done
	})
}

func runSourceMetadataTask[T any](a *App, job *sourceMetadataTask[T], unpin func(), current func() bool, prune func(), load func(func(int64, int64, string)) (*T, error)) {
	var snapshot *T
	var err error
	defer func() {
		unpin()
		a.mu.Lock()
		defer a.mu.Unlock()
		if job.ctx.Err() != nil || !current() || job.discarded {
			job.Status, job.Error, job.Stage = "cancelled", "", "已取消"
		} else if err != nil {
			job.Status, job.Error, job.Stage = "error", err.Error(), "stage.task.unreadReading"
		} else if snapshot == nil {
			job.Status, job.Error, job.Stage = "error", i18n.Default.Text("service.metadata.resultMissing"), "stage.task.unreadReading"
		} else {
			job.Status, job.Stage, job.Snapshot = "done", "stage.task.readingCompleted", snapshot
		}
		close(job.done)
		prune()
	}()
	releaseScan, admissionErr := a.backgroundQuery(job.ctx)
	if admissionErr != nil {
		return
	}
	defer releaseScan()
	a.mu.Lock()
	if job.ctx.Err() != nil || !current() || job.discarded {
		a.mu.Unlock()
		return
	}
	job.Status, job.Stage = "running", "stage.task.readingInProgress"
	a.mu.Unlock()
	if err = job.Workbook.CheckSourceUnchanged(); err != nil {
		return
	}
	if err = job.ctx.Err(); err != nil {
		return
	}
	snapshot, err = load(func(done, total int64, stage string) {
		a.mu.Lock()
		job.DoneBytes, job.TotalBytes, job.Stage = done, total, stage
		a.mu.Unlock()
	})
	if err == nil {
		err = job.Workbook.CheckSourceUnchanged()
	}
}

func pruneMetadataTasks[T any](jobs map[string]*sourceMetadataTask[T], keep string, limits metadataTaskLimits, size func(*T) int64) {
	taskmanager.PruneBudgeted(jobs, keep, taskmanager.BudgetLimits{
		History:   limits.history,
		Snapshots: limits.snapshots,
		Bytes:     limits.bytes,
	}, func(job *sourceMetadataTask[T]) taskmanager.BudgetEntry {
		entry := taskmanager.BudgetEntry{
			Created:  job.Created,
			Done:     job.done,
			Cancel:   job.cancel,
			Retained: job.Snapshot != nil,
		}
		if job.Snapshot != nil {
			entry.Bytes = size(job.Snapshot)
		}
		return entry
	})
}

func clearMetadataTasks[T any](jobs map[string]*sourceMetadataTask[T]) []*sourceMetadataTask[T] {
	all := make([]*sourceMetadataTask[T], 0, len(jobs))
	for id, job := range jobs {
		job.cancel()
		job.discarded, job.Snapshot = true, nil
		all = append(all, job)
		delete(jobs, id)
	}
	return all
}

func waitMetadataTasks[T any](jobs []*sourceMetadataTask[T]) {
	for _, job := range jobs {
		<-job.done
	}
}

// Caller holds App.mu. A cancelled late start can already be complete; remove
// its unadopted snapshot too, so it cannot evict the client's accepted result.
func metadataTaskAction[T any](jobs map[string]*sourceMetadataTask[T], id string, clear bool) bool {
	job := jobs[id]
	if job == nil {
		return false
	}
	if clear {
		job.discarded, job.Snapshot = true, nil
	}
	job.cancel()
	select {
	case <-job.done:
		job.discarded, job.Snapshot = true, nil
		delete(jobs, id)
	default:
	}
	return true
}

func metadataTaskPayload[T any](locale i18n.Locale, job *sourceMetadataTask[T]) map[string]any {
	return map[string]any{"id": job.ID, "status": job.Status, "stage": locale.Stage(job.Stage), "error": job.Error, "doneBytes": job.DoneBytes, "totalBytes": job.TotalBytes, "workbookKey": job.Workbook.Fingerprint, "book": sourceBookID(job.Workbook), "sheet": job.Meta.Name}
}

func metadataActionID(w http.ResponseWriter, r *http.Request, limit int64) (string, error) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return "", errors.New("method")
	}
	var req struct {
		ID string `json:"id"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil || decodeSavedConfig(data, &req) != nil || req.ID == "" {
		writeMessage(w, 400, "service.sourceMetadataTasks.invalidReadingTaskIdentifier")
		return "", errors.New("request")
	}
	return req.ID, nil
}
