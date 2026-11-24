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
	ID       string              `json:"id"`
	Action   string              `json:"action"`
	State    string              `json:"state"`
	Progress localstore.Progress `json:"progress"`
	Error    string              `json:"error,omitempty"`
	Started  time.Time           `json:"started"`
	Finished time.Time           `json:"finished,omitzero"`
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
			jobs[i].Error = "程序退出前未记录完成，请核对目标后重试"
		}
	}
	s.jobs, s.loaded = jobs, true
	return nil
}
func (s *storageJobs) stop() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}
func (a *App) startStorage(roots localstore.Roots, action, dir, password string) (storageJob, error) {
	return a.submitStorage(roots, action, func(ctx context.Context, report func(localstore.Progress)) (transferResult, error) {
		options := localstore.TransferOptions{Password: password, Report: report}
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
func (a *App) submitStorage(roots localstore.Roots, action string, run func(context.Context, func(localstore.Progress)) (transferResult, error)) (storageJob, error) {

	s := &a.transfers
	s.mu.Lock()
	defer s.mu.Unlock()
	var empty storageJob
	if s.closed {
		return empty, errors.New("程序正在退出")
	}
	if a.restorePending.Load() && action != "restore" {
		return empty, errors.New("请先完成恢复事务")
	}
	file := filepath.Join(roots.Config, "transfer-history.json")
	if err := s.load(file); err != nil {
		return empty, err
	}
	job := storageJob{ID: randomID(), Action: action, State: "running", Started: time.Now().UTC(), Progress: localstore.Progress{Phase: "准备"}}
	jobs := append([]storageJob{job}, s.jobs[:min(len(s.jobs), 31)]...)
	if err := writeSavedConfig(file, 1<<20, jobs, durableConfigOps); err != nil {
		return empty, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.jobs, s.cancel, s.done = jobs, cancel, make(chan struct{})
	go func() {
		defer cancel()
		result, err := run(ctx, func(p localstore.Progress) { s.mu.Lock(); s.jobs[0].Progress = p; s.mu.Unlock() })
		s.mu.Lock()
		current := &s.jobs[0]
		current.Finished = time.Now().UTC()
		if err != nil {
			current.State, current.Error = "failed", err.Error()
			if errors.Is(err, context.Canceled) {
				current.State, current.Error = "cancelled", "已取消"
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
		if s.jobs[0].Progress.Phase == "提交恢复事务" {
			writeError(w, 409, "已开始提交恢复事务")
			return
		}
		s.cancel()
		writeJSON(w, map[string]bool{"cancelled": true})
	default:
		methodNotAllowed(w)
	}
}
func (a *App) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	roots, err := dataRoots()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	var req struct {
		Book, Previous, Path string
		Confirm              bool
	}
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 96<<10))
		if err != nil || decodeSavedConfig(data, &req) != nil || !validExportDigest(req.Book) || !filepath.IsAbs(req.Path) {
			writeError(w, 400, "来源定位参数无效")
			return
		}
	}
	if !a.storageMu.TryLock() {
		writeError(w, 409, "另一项数据管理操作正在进行")
		return
	}
	if a.restorePending.Load() {
		a.storageMu.Unlock()
		writeError(w, 409, "请先完成恢复事务")
		return
	}
	if r.Method == http.MethodGet {
		defer a.storageMu.Unlock()
		items, err := relocationSources(r.Context())
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]any{"items": items})
		return
	}
	action := "verify-source"
	if req.Confirm {
		action = "relocate-source"
	}
	job, err := a.submitStorage(roots, action, func(ctx context.Context, report func(localstore.Progress)) (transferResult, error) {
		report(localstore.Progress{Phase: "核验来源指纹"})
		item, err := a.verifyLocation(ctx, req.Book, req.Previous, req.Path, req.Confirm)
		if err != nil {
			return transferResult{}, err
		}
		msg := "内容指纹相符"
		if req.Confirm {
			msg = "已更新来源位置"
		}
		return transferResult{Message: msg, Candidate: item.Candidate}, nil
	})
	if err != nil {
		a.storageMu.Unlock()
		writeError(w, 409, err.Error())
		return
	}
	writeJSONStatus(w, 202, job)
}
