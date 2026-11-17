package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

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
	active := 0
	for _, job := range jobs {
		select {
		case <-job.done:
		default:
			active++
		}
	}
	return active
}

func runSourceMetadataTask[T any](a *App, job *sourceMetadataTask[T], unpin func(), label string, current func() bool, prune func(), load func(func(int64, int64, string)) (*T, error)) {
	var snapshot *T
	var err error
	defer func() {
		unpin()
		a.mu.Lock()
		defer a.mu.Unlock()
		if job.ctx.Err() != nil || !current() || job.discarded {
			job.Status, job.Error, job.Stage = "cancelled", label+"读取已取消", "已取消"
		} else if err != nil {
			job.Status, job.Error, job.Stage = "error", err.Error(), "读取未完成"
		} else if snapshot == nil {
			job.Status, job.Error, job.Stage = "error", "未取得完整"+label, "读取未完成"
		} else {
			job.Status, job.Stage, job.Snapshot = "done", label+"读取完成", snapshot
		}
		close(job.done)
		prune()
	}()
	select {
	case a.querySem <- struct{}{}:
		defer func() { <-a.querySem }()
	case <-job.ctx.Done():
		return
	}
	a.mu.Lock()
	if job.ctx.Err() != nil || !current() || job.discarded {
		a.mu.Unlock()
		return
	}
	job.Status, job.Stage = "running", "正在读取"+label
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
	for {
		count, bytes := 0, int64(0)
		oldest := ""
		for id, job := range jobs {
			if job.Snapshot != nil {
				count++
				bytes += size(job.Snapshot)
			}
			select {
			case <-job.done:
				if id != keep && (oldest == "" || job.Created.Before(jobs[oldest].Created)) {
					oldest = id
				}
			default:
			}
		}
		if len(jobs) <= limits.history && count <= limits.snapshots && bytes <= limits.bytes || oldest == "" {
			return
		}
		jobs[oldest].cancel()
		delete(jobs, oldest)
	}
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

func metadataTaskPayload[T any](job *sourceMetadataTask[T]) map[string]any {
	return map[string]any{"id": job.ID, "status": job.Status, "stage": job.Stage, "error": job.Error, "doneBytes": job.DoneBytes, "totalBytes": job.TotalBytes, "workbookKey": job.Workbook.Fingerprint, "book": sourceBookID(job.Workbook), "sheet": job.Meta.Name}
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
		writeError(w, 400, "读取任务标识无效")
		return "", errors.New("request")
	}
	return req.ID, nil
}
