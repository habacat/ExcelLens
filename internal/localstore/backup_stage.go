package localstore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"excellens/internal/filelock"
)

const stagePayload = "payload"

type stageLayout struct {
	pattern           *regexp.Regexp
	marker, signature string
}

var backupStageLayout = stageLayout{regexp.MustCompile(`^\.ExcelLens-[0-9]{8}-[0-9]{6}-[0-9]+$`), ".excellens-backup-stage", "ExcelLens backup stage v1"}
var snapshotStageLayout = stageLayout{regexp.MustCompile(`^\.snapshot-[0-9]+\.tmp$`), ".excellens-snapshot-stage", "ExcelLens database snapshot stage v1"}

type BackupStage struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func (layout stageLayout) markerText(name string) string {
	return layout.signature + "\n" + name + "\n"
}

// Keep the lease outside the payload directory so Windows can publish it
// atomically while the writer still excludes cleanup by another process.
func newBackupStage(parent string) (string, func() error, error) {
	return backupStageLayout.create(parent, ".ExcelLens-"+time.Now().Format("20060102-150405")+"-")
}

// NewSnapshotStage returns the private payload and a cleanup function which
// holds the writer lease until after publication or discard.
func NewSnapshotStage(parent string) (string, func() error, error) {
	stage, cleanup, err := snapshotStageLayout.create(parent, ".snapshot-*.tmp")
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(stage, stagePayload), cleanup, nil
}

func (layout stageLayout) create(parent, pattern string) (string, func() error, error) {
	stage, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", nil, err
	}
	marker, err := os.OpenFile(filepath.Join(stage, layout.marker), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		os.Remove(stage)
		return "", nil, err
	}
	cleanup := func() error {
		removeErr := os.RemoveAll(filepath.Join(stage, stagePayload))
		if err := errors.Join(removeErr, marker.Close()); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(stage, layout.marker)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Remove(stage); !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err = filelock.Lock(marker); err == nil {
		_, err = io.WriteString(marker, layout.markerText(filepath.Base(stage)))
	}
	if err == nil {
		err = marker.Sync()
	}
	if err == nil {
		err = os.Mkdir(filepath.Join(stage, stagePayload), 0700)
	}
	if err == nil {
		err = syncDir(stage)
	}
	if err != nil {
		return "", nil, errors.Join(err, cleanup())
	}
	return stage, cleanup, nil
}

func (layout stageLayout) inspect(parent *os.Root, name string, clean bool) (string, error) {
	if !layout.pattern.MatchString(name) {
		return "", errors.New("暂存目录名称无效")
	}
	st, err := parent.Lstat(name)
	if err != nil || !st.IsDir() {
		return "unknown", err
	}
	stage, err := parent.OpenRoot(name)
	if err != nil {
		return "", err
	}
	defer stage.Close()
	marker, _, err := openRegular(stage, layout.marker)
	if errors.Is(err, os.ErrNotExist) {
		return "unknown", nil
	}
	if err != nil {
		return "", err
	}
	defer marker.Close()
	if err = filelock.Lock(marker); errors.Is(err, filelock.ErrBusy) {
		return "active", nil
	} else if err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(marker, 256))
	if err != nil {
		return "", err
	}
	if string(b) != layout.markerText(name) {
		return "unknown", nil
	}
	if !clean {
		return "orphan", nil
	}
	// Remove only the owned payload and marker. Preserve all other entries.
	entries, err := fs.ReadDir(stage.FS(), ".")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Name() != stagePayload && e.Name() != layout.marker {
			return "", errors.New("暂存目录含其他文件，请先核对")
		}
	}
	if err := stage.RemoveAll(stagePayload); err != nil {
		return "", err
	}
	if err := marker.Close(); err != nil {
		return "", err
	}
	if err := stage.Remove(layout.marker); err != nil {
		return "", err
	}
	if err := stage.Close(); err != nil {
		return "", err
	}
	current, err := parent.Lstat(name)
	if err != nil {
		return "", err
	}
	if !os.SameFile(st, current) {
		return "", errors.New("暂存目录已更换")
	}
	return "removed", parent.Remove(name)
}
func (r Roots) BackupStages(folder string) ([]BackupStage, error) {
	path, err := r.externalFolder(folder)
	if err != nil {
		return nil, err
	}
	root, err := openDirectory(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := []BackupStage{}
	for _, e := range entries {
		if !e.IsDir() || !backupStageLayout.pattern.MatchString(e.Name()) {
			continue
		}
		state, err := backupStageLayout.inspect(root, e.Name(), false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, BackupStage{e.Name(), state})
	}
	return out, nil
}
func (r Roots) CleanBackupStage(folder, name string) error {
	path, err := r.externalFolder(folder)
	if err != nil {
		return err
	}
	root, err := openDirectory(path)
	if err != nil {
		return err
	}
	defer root.Close()
	state, err := backupStageLayout.inspect(root, name, true)
	if err != nil {
		return err
	}
	if state != "removed" {
		return fmt.Errorf("暂存目录仍在使用或缺少所有权标记：%s", name)
	}
	return nil
}
