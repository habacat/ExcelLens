package localstore

import (
	"context"
	"encoding/hex"
	"errors"

	"io"
	"os"
	"path/filepath"
	"strings"

	"excellens/internal/i18n"
)

type Migration struct {
	Copied    int `json:"copied"`
	Identical int `json:"identical"`
	Different int `json:"different"`
}

func legacyName(name string) bool {
	key, ok := strings.CutSuffix(name, ".json")
	if !ok || (len(key) != 40 && len(key) != 64) {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

// Adopt copies a historical workbook's settings only if its current version
// is absent. Caller serializes reads and writes for this file.
func (r Roots) Adopt(ctx context.Context, kind, name string) (bool, error) {
	if (kind != "annotations" && kind != "layouts" && kind != "frozen-panes") || !legacyName(name) {
		return false, i18n.Error("diagnostic.localstore.migration.oldVersionFileNameInvalid")
	}
	for _, dir := range []string{r.Config, filepath.Join(r.Config, kind)} {
		st, err := os.Lstat(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if err == nil && !st.IsDir() {
			return false, i18n.Error("diagnostic.localstore.migration.setDirectoryNotRegularDirectory", i18n.Args{"dir": dir})
		}
	}
	target := filepath.Join(r.Config, kind, name)
	if st, err := os.Lstat(target); err == nil {
		if !st.Mode().IsRegular() {
			return false, i18n.Error("diagnostic.localstore.migration.settingNotRegularFile", i18n.Args{"target": target})
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	rel := filepath.Join(kind, name)
	if kind == "frozen-panes" {
		rel = filepath.Join("cache", strings.TrimSuffix(name, ".json"), "view.json")
	}
	if _, err := os.Lstat(filepath.Join(r.Local, rel)); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	src, err := openDirectory(r.Local)
	if err != nil {
		return false, err
	}
	defer src.Close()
	if err := os.MkdirAll(r.Config, 0700); err != nil {
		return false, err
	}
	dst, err := openDirectory(r.Config)
	if err != nil {
		return false, err
	}
	defer dst.Close()
	if err := dst.MkdirAll(kind, 0700); err != nil {
		return false, err
	}
	dir, err := openDirectory(filepath.Join(r.Config, kind))
	if err != nil {
		return false, err
	}
	defer dir.Close()
	tmp, err := os.CreateTemp(dir.Name(), ".migrate-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	rec, err := readFile(ctx, src, rel, tmp)
	if err == nil {
		err = tmp.Sync()
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return false, err
	}
	check, err := readFile(ctx, dir, filepath.Base(tmp.Name()), io.Discard)
	if err != nil {
		return false, err
	}
	if check != rec {
		return false, i18n.Error("diagnostic.localstore.migration.failedCopySettingsFromOlder")
	}
	if err := publishNew(tmp.Name(), target); errors.Is(err, os.ErrExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func (r Roots) MigrateLegacy(ctx context.Context) (Migration, error) {
	return r.MigrateLegacyWith(ctx, TransferOptions{})
}
func (r Roots) MigrateLegacyWith(ctx context.Context, options TransferOptions) (Migration, error) {
	ctx = options.start(ctx)
	phase(ctx, "stage.task.migratingToOldSettings", 0, 0)
	var report Migration
	if err := r.migrateFrozen(ctx, &report); err != nil {
		return report, err
	}
	if sameDirectory(r.Local, r.Config) {
		return report, nil
	}
	for _, kind := range []string{"annotations", "layouts"} {
		dir, err := openDirectory(filepath.Join(r.Local, kind))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return report, err
		}
		entries, err := os.ReadDir(dir.Name())
		dir.Close()
		if err != nil {
			return report, err
		}
		for _, entry := range entries {
			if !legacyName(entry.Name()) {
				continue
			}
			if err := r.migrateFile(ctx, kind, entry.Name(), filepath.Join(kind, entry.Name()), &report); err != nil {
				return report, err
			}
		}
	}
	return report, nil
}

func digestAt(ctx context.Context, base, name string) (fileRecord, error) {
	root, err := openDirectory(base)
	if err != nil {
		return fileRecord{}, err
	}
	defer root.Close()
	return readFile(ctx, root, name, io.Discard)
}

// Historical freeze overrides are the only user preferences inside workbook
// index groups. Inspect the known filename, without reading sheet indexes.
func (r Roots) AdoptFrozen(ctx context.Context) error { return r.migrateFrozen(ctx, nil) }

func (r Roots) migrateFile(ctx context.Context, kind, name, source string, report *Migration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	defer finishFile(ctx)
	copied, err := r.Adopt(ctx, kind, name)
	if err != nil || report == nil {
		return err
	}
	if copied {
		report.Copied++
		return nil
	}
	a, err := digestAt(ctx, r.Local, source)
	if err != nil {
		return err
	}
	b, err := digestAt(ctx, r.Config, filepath.Join(kind, name))
	if err != nil {
		return err
	}
	if a == b {
		report.Identical++
	} else {
		report.Different++
	}
	return nil
}

func (r Roots) migrateFrozen(ctx context.Context, report *Migration) error {
	cache, err := openDirectory(filepath.Join(r.Local, "cache"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer cache.Close()
	entries, err := os.ReadDir(cache.Name())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name() + ".json"
		if !entry.IsDir() || !legacyName(name) {
			continue
		}
		rel := filepath.Join(entry.Name(), "view.json")
		if _, err := cache.Lstat(rel); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := r.migrateFile(ctx, "frozen-panes", name, filepath.Join("cache", rel), report); err != nil {
			return err
		}
	}
	return nil
}
