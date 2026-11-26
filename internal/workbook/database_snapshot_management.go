package workbook

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"excellens/internal/localstore"
)

// Measure actual files, including the manifest and damaged or old captures.
// The catalogue does not read row contents or build a query cache.
func snapshotDirectorySize(ctx context.Context, dir string) (int64, error) {
	var size int64
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
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
			return fmt.Errorf("快照目录含非常规文件：%s", e.Name())
		}
		size += info.Size()
		return nil
	})
	return size, err
}

func managedSnapshot(key string) (databaseManifest, string, error) {
	path := DatabaseSnapshotPath(key)
	if path == "" {
		return databaseManifest{}, "", errors.New("数据库快照标识无效")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		path = LegacyDatabaseSnapshotPath(key)
	}
	meta, _, _, err := databaseManifestAt(path)
	return meta, path, err
}

// RemoveDatabaseSnapshot moves an explicitly unreferenced capture out of the
// catalogue in one rename. A process exit afterwards leaves a leased stage
// that the normal startup recovery can finish. Unknown siblings stay intact.
func RemoveDatabaseSnapshot(ctx context.Context, key string) error {
	meta, path, err := managedSnapshot(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	parent := filepath.Dir(dir)
	for _, p := range []string{parent, DatabaseSnapshotRoot()} {
		if err := os.MkdirAll(p, 0700); err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() {
			return errors.New("快照存放位置已改变")
		}
	}
	owned := map[string]bool{databaseSnapshotFile: true}
	wb := databaseWorkbook(meta, key, dir)
	for _, sheet := range wb.Sheets {
		rows, index := wb.cachePaths(sheet.Path)
		owned[filepath.Base(rows)], owned[filepath.Base(index)] = true, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !owned[entry.Name()] || !entry.Type().IsRegular() {
			return fmt.Errorf("快照目录含其他文件，请先核对：%s", entry.Name())
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, cleanup, err := localstore.NewSnapshotStage(DatabaseSnapshotRoot())
	if err != nil {
		return err
	}
	if err := os.Rename(dir, filepath.Join(payload, key)); err != nil {
		return errors.Join(err, cleanup())
	}
	if err := cleanup(); err != nil {
		return fmt.Errorf("快照已从列表移除，暂存空间尚待释放：%w", err)
	}
	return nil
}
