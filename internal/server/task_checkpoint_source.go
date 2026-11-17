package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type taskCheckpointRun struct {
	store   *checkpoint.Store
	lease   *checkpoint.Lease
	release func()
}

func (run *taskCheckpointRun) close() error {
	err := run.lease.Close()
	err = errors.Join(err, run.store.Close())
	run.release()
	return err
}

// A rejected new start never published its ID or ran a worker. Discard only
// that lease's exact revision; a rejected resume must retain user progress.
func (run *taskCheckpointRun) reject(created bool) error {
	m := run.lease.Snapshot()
	err := run.lease.Close()
	if created && err == nil {
		err = run.store.DiscardRevision(m.ID, m.Revision)
	}
	err = errors.Join(err, run.store.Close())
	run.release()
	return err
}

func openTaskCheckpoints() (*checkpoint.Store, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return nil, errors.New("无法定位用户配置目录，任务恢复尚未保存")
	}
	return checkpoint.Open(filepath.Join(base, "ExcelLens", "task-checkpoints"))
}

func checkpointWorkbookSource(wb *workbook.Workbook, source, stamp string) (checkpoint.Source, error) {
	if wb == nil {
		return checkpoint.Source{}, checkpoint.ErrBinding
	}
	path, err := filepath.Abs(wb.Path)
	if err != nil {
		return checkpoint.Source{}, err
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return checkpoint.Source{Path: path, Book: sourceBookID(wb), Fingerprint: wb.Fingerprint, Format: wb.Format, Source: source, Stamp: stamp}, nil
}

func checkpointTitle(prefix, path string) string {
	title := []rune(prefix + " · " + filepath.Base(path))
	if len(title) > formats.CheckpointTitleRunes {
		title = append(title[:formats.CheckpointTitleRunes-1], '…')
	}
	return string(title)
}

func checkpointHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, checkpoint.ErrBusy), errors.Is(err, checkpoint.ErrConflict), errors.Is(err, checkpoint.ErrBinding), errors.Is(err, errExportCaptureIncomplete), errors.Is(err, excelExport.ErrResumeState):
		status = http.StatusConflict
	case errors.Is(err, checkpoint.ErrCapacity):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, checkpoint.ErrCorrupt):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
	}
	writeError(w, status, err.Error())
}

func (a *App) admitCheckpoint(w http.ResponseWriter) bool {
	select {
	case a.rootApp().recoverySem <- struct{}{}:
		return true
	default:
		writeError(w, http.StatusTooManyRequests, "任务恢复繁忙，请稍后重试")
		return false
	}
}
