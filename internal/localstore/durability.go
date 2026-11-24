package localstore

import (
	"io/fs"
	"path/filepath"
	"slices"
)

func syncTree(path string) error {
	var dirs []string
	if err := filepath.WalkDir(path, func(name string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			dirs = append(dirs, name)
		}
		return err
	}); err != nil {
		return err
	}
	for _, dir := range slices.Backward(dirs) {
		if err := syncDir(dir); err != nil {
			return err
		}
	}
	return nil
}
