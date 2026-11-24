package server

import (
	"context"
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

// Verify the physical source before canonicalizing the path for the immutable
// definition hash. All driver semantics, rules and sheet bindings still match.
func bindTaskSource(ctx context.Context, wb *workbook.Workbook, actual, saved checkpoint.Source, verifyRaw bool) error {
	loc, err := loadSourceLocation(saved)
	if err != nil {
		return err
	}
	physical := loc.current(saved.Path)
	if cachePathKey(actual.Path) != cachePathKey(physical) || actual.Book != bookIDAt(saved.Fingerprint, physical) {
		return checkpoint.ErrBinding
	}
	raw := saved.SHA256
	if loc.SHA256 != "" {
		if raw != "" && raw != loc.SHA256 {
			return checkpoint.ErrBinding
		}
		raw = loc.SHA256
	}
	if verifyRaw && raw != "" {
		hash, err := wb.SourceFileSHA256(ctx)
		if err != nil {
			return err
		}
		if hash != raw {
			return checkpoint.ErrBinding
		}
	}
	actual.Path, actual.Book, actual.SHA256 = saved.Path, saved.Book, saved.SHA256
	if actual != saved {
		return checkpoint.ErrBinding
	}
	return nil
}
func taskBinding(ctx context.Context, wb *workbook.Workbook, def checkpoint.Definition, m checkpoint.Manifest, verifyRaw bool) (string, error) {
	if len(def.Sources) != 1 || len(m.Definition.Sources) != 1 {
		return "", checkpoint.ErrBinding
	}
	actual, saved := def.Sources[0], m.Definition.Sources[0]
	if def.Driver == exportCheckpointDriver {
		var err error
		actual, err = exportTaskSource(wb)
		if err != nil {
			return "", err
		}
	}
	if err := bindTaskSource(ctx, wb, actual, saved, verifyRaw); err != nil {
		return "", err
	}
	if cachePathKey(actual.Path) != cachePathKey(saved.Path) {
		def.Title = m.Definition.Title
	}
	def.Sources = []checkpoint.Source{saved}
	return checkpoint.DefinitionHash(def)
}
func setTaskDigest(ctx context.Context, wb *workbook.Workbook, def *checkpoint.Definition) error {
	digest, err := wb.SourceFileSHA256(ctx)
	if err == nil {
		def.Sources[0].SHA256 = digest
	}
	return err
}
