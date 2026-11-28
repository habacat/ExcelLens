package localstore

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/i18n"
)

type SnapshotStage struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Bytes     int64  `json:"bytes"`
	Files     int    `json:"files"`
	Error     string `json:"error,omitempty"`
	allocated int64
}

func snapshotStageName(name string) bool {
	return strings.HasPrefix(name, ".snapshot-") && strings.HasSuffix(name, ".tmp")
}

// At startup only a valid owner marker and an obtainable writer lock allow
// removal. Old captures have no lease and may still belong to another process.
func (r Roots) SnapshotStages(ctx context.Context, recoverOrphans bool) ([]SnapshotStage, error) {
	root, err := openDirectory(filepath.Join(r.Local, formats.DatabaseSnapshotDirectory))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var out []SnapshotStage
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !snapshotStageName(entry.Name()) {
			continue
		}
		s := SnapshotStage{Name: entry.Name(), State: "unknown"}
		var inspectErr error
		if snapshotStageLayout.pattern.MatchString(s.Name) {
			s.State, inspectErr = snapshotStageLayout.inspect(root, s.Name, recoverOrphans)
		}
		if errors.Is(inspectErr, os.ErrNotExist) || s.State == "removed" {
			continue
		}
		if !recoverOrphans {
			err := fs.WalkDir(root.FS(), s.Name, func(name string, e fs.DirEntry, err error) error {
				if err != nil || ctx.Err() != nil {
					return errors.Join(err, ctx.Err())
				}
				if e.IsDir() {
					return nil
				}
				info, err := e.Info()
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return i18n.Error("diagnostic.localstore.snapshotStage.temporaryDirectoryContainsNonStandard")
				}
				s.Bytes += info.Size()
				s.Files++
				size, err := allocatedSize(root, filepath.FromSlash(name), info)
				s.allocated += size
				return err
			})
			if errors.Is(err, os.ErrNotExist) {
				if _, e := root.Lstat(s.Name); errors.Is(e, os.ErrNotExist) {
					continue
				}
			}
			inspectErr = errors.Join(inspectErr, err)
		}
		if inspectErr != nil {
			s.Error = inspectErr.Error()
		}
		out = append(out, s)
	}
	return out, nil
}
