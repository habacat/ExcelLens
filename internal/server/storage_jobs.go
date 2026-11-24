package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"excellens/internal/localstore"
)

type transferResult struct {
	Message   string `json:"message,omitempty"`
	Candidate string `json:"candidate,omitempty"`
}
type storageJob struct {
	transferResult
	ID          string              `json:"id"`
	Action      string              `json:"action"`
	State       string              `json:"state"`
	Transaction string              `json:"transaction"`
	Progress    localstore.Progress `json:"progress"`
	Error       string              `json:"error,omitempty"`
	Started     time.Time           `json:"started"`
	Finished    time.Time           `json:"finished,omitzero"`
}
type storageJobs struct {
	mu             sync.Mutex
	jobs           []storageJob
	loaded, closed bool
	cancel         context.CancelFunc
	done           chan struct{}
}

func (s *storageJobs) load(file string) error {
	if s.loaded {
		return nil
	}
	var jobs []storageJob
	if _, err := readSavedConfig(file, 1<<20, &jobs); err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].State == "running" {
			jobs[i].State = "interrupted"
			jobs[i].Transaction = "finished"
			jobs[i].Error = "程序退出前未记录完成，请核对目标后重试"
		}
	}
	s.jobs, s.loaded = jobs, true
	return nil
}
func (s *storageJobs) stop() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil && s.jobs[0].Transaction != "committing" {
		s.jobs[0].Transaction = "cancelling"
		s.cancel()
	}
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}
func (a *App) startStorage(roots localstore.Roots, action, dir, password string) (storageJob, error) {
	return a.submitStorage(roots, action, func(ctx context.Context, options localstore.TransferOptions) (transferResult, error) {
		options.Password = password
		var msg string
		var err error
		switch action {
		case "restore":
			err = a.restoreStorage(ctx, roots, dir, options)
			msg = "已核验并迁入备份，刷新页面可读取恢复记录"
		case "backup":
			a.cacheResetMu.RLock()
			var path string
			var missing int
			missing, err = a.prepareLegacyTasks(ctx)
			if err == nil {
				path, err = roots.BackupWith(ctx, dir, options)
			}
			a.cacheResetMu.RUnlock()
			msg = "备份已核验：" + path
			if missing > 0 {
				msg += fmt.Sprintf("（%d 项旧任务缺少可迁移的来源或索引证明）", missing)
			}
		case "migrate":
			a.cacheResetMu.RLock()
			var report localstore.Migration
			report, err = roots.MigrateLegacyWith(ctx, options)
			a.cacheResetMu.RUnlock()
			msg = fmt.Sprintf("已迁入 %d 个，相同 %d 个，内容不同且保留两份 %d 个", report.Copied, report.Identical, report.Different)
		}
		return transferResult{Message: msg}, err
	})
}
func (a *App) submitStorage(roots localstore.Roots, action string, run func(context.Context, localstore.TransferOptions) (transferResult, error)) (storageJob, error) {

	s := &a.transfers
	s.mu.Lock()
	defer s.mu.Unlock()
	var empty storageJob
	if s.closed {
		return empty, errors.New("程序正在退出")
	}
	if a.restoreState.pending() && action != "restore" {
		return empty, errors.New("请先完成恢复事务")
	}
	file := filepath.Join(roots.Config, "transfer-history.json")
	if err := s.load(file); err != nil {
		return empty, err
	}
	job := storageJob{ID: randomID(), Action: action, State: "running", Transaction: "preparing", Started: time.Now().UTC(), Progress: localstore.Progress{Phase: "准备"}}
	jobs := append([]storageJob{job}, s.jobs[:min(len(s.jobs), 31)]...)
	if err := writeSavedConfig(file, 1<<20, jobs, durableConfigOps); err != nil {
		return empty, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.jobs, s.cancel, s.done = jobs, cancel, make(chan struct{})
	go func() {
		defer cancel()
		s.mu.Lock()
		if s.jobs[0].Transaction == "preparing" {
			s.jobs[0].Transaction = "cancellable"
		}
		s.mu.Unlock()
		options := localstore.TransferOptions{
			Report: func(p localstore.Progress) { s.mu.Lock(); s.jobs[0].Progress = p; s.mu.Unlock() },
			BeginCommit: func() error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if err := ctx.Err(); err != nil {
					return err
				}
				s.jobs[0].Transaction = "committing"
				return nil
			},
		}
		a.storageView.Lock()
		result, err := run(ctx, options)
		a.storageView.Unlock()
		s.mu.Lock()
		current := &s.jobs[0]
		if current.Transaction == "cancelling" && err == nil {
			err = context.Canceled
		}
		current.Transaction = "finished"
		current.Finished = time.Now().UTC()
		if err != nil {
			current.State, current.Error = "failed", err.Error()
			if errors.Is(err, context.Canceled) {
				current.State = "cancelled"
				if err == context.Canceled {
					current.Error = "已取消"
				}
			}
		} else {
			current.State, current.transferResult = "done", result
		}
		if e := writeSavedConfig(file, 1<<20, s.jobs, durableConfigOps); e != nil {
			current.Error = errors.Join(err, fmt.Errorf("完成状态未能写入磁盘：%w", e)).Error()
		}
		close(s.done)
		s.cancel, s.done = nil, nil
		a.storageMu.Unlock()
		s.mu.Unlock()
	}()
	return job, nil
}
func (a *App) handleStorageJobs(w http.ResponseWriter, r *http.Request) {
	roots, err := dataRoots()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256))
		var empty struct{}
		if err != nil || len(body) > 0 && decodeSavedConfig(body, &empty) != nil {
			writeError(w, 400, "取消参数无效")
			return
		}
	}
	s := &a.transfers
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(filepath.Join(roots.Config, "transfer-history.json")); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	id := r.URL.Query().Get("id")
	switch r.Method {
	case http.MethodGet:
		if id == "" {
			writeJSON(w, map[string]any{"jobs": append([]storageJob{}, s.jobs...)})
			return
		}
		for _, job := range s.jobs {
			if job.ID == id {
				writeJSON(w, job)
				return
			}
		}
		writeError(w, 404, "传输任务不存在")
	case http.MethodPost:
		for _, job := range s.jobs {
			if job.ID == id && job.State == "cancelled" {
				writeJSON(w, map[string]bool{"cancelled": true})
				return
			}
		}
		if len(s.jobs) == 0 || id == "" || s.jobs[0].ID != id || s.cancel == nil {
			writeError(w, 409, "任务已结束或不存在")
			return
		}
		if s.jobs[0].Transaction == "committing" {
			writeError(w, 409, "已进入提交阶段，无法取消")
			return
		}
		s.jobs[0].Transaction = "cancelling"
		s.cancel()
		writeJSON(w, map[string]bool{"cancelled": true})
	default:
		methodNotAllowed(w)
	}
}
