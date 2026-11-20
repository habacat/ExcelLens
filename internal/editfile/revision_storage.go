package editfile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

var revisionFilename = regexp.MustCompile("^r[1-9][0-9]*\\.(xlsx|xlsm)$")

func reserveRevision(ctx context.Context, doc *editdoc.Document, dir, next string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var total int64
	sizes := map[string]int64{}
	for _, e := range entries {
		if e.Name() == filepath.Base(next) || e.Name() == ".overlay" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if info.Mode().IsRegular() && revisionFilename.MatchString(e.Name()) {
			sizes[filepath.Join(dir, e.Name())] = info.Size()
		}
	}
	if total <= formats.EditStructureHistoryBytes {
		return nil
	}
	if filepath.Base(dir) != doc.ID {
		return ErrBinding
	}
	clean := func() error {
		keep := doc.ReferencedBasePaths()
		for path, size := range sizes {
			if keep[path] || strings.EqualFold(path, doc.OriginalBase().Source.Path) {
				continue
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			total -= size
			delete(sizes, path)
		}
		return nil
	}
	if err := clean(); err != nil {
		return err
	}
	for _, keep := range []int{32, 8, 1} {
		if total <= formats.EditStructureHistoryBytes {
			return nil
		}
		if err := doc.Compact(ctx, keep); err != nil {
			return err
		}
		if err := clean(); err != nil {
			return err
		}
	}
	if total > formats.EditStructureHistoryBytes {
		return fmt.Errorf("当前工作簿与最近修订超过 %d GB", formats.EditStructureHistoryBytes>>30)
	}
	return nil
}
