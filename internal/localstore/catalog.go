// Package localstore inventories application-owned data and transfers user files.
package localstore

import (
	"context"
	"errors"

	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/i18n"
)

func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "", i18n.Error("diagnostic.localstore.catalog.unableLocateUserConfigurationDirectory")
	}
	return filepath.Join(base, "ExcelLens"), nil
}

type Roots struct {
	Local  string `json:"local"`
	Config string `json:"config"`
}

type group struct {
	name, kind i18n.Key
	base       string
	parts      []string
	backup     bool
}

func (r Roots) groups() []group {
	rows := []group{
		{"service.storageInventory.workbookIndexes", "service.storageInventory.rebuildableCache", "local", []string{"cache"}, false},
		{"service.storageInventory.conditionalStatistics", "service.storageInventory.rebuildableCache", "local", []string{formats.ConditionalStatsDirectory}, false},
		{"service.storageInventory.databaseSnapshots", "service.storageInventory.auditSource", "local", []string{formats.DatabaseSnapshotDirectory}, true},
		{"service.storageInventory.uploadedCopies", "service.storageInventory.sourceCopy", "local", []string{"temp"}, true},
		{"service.storageInventory.exports", "service.storageInventory.workProduct", "local", []string{"exports"}, true},
		{"service.storageInventory.calculationHistory", "service.storageInventory.workProduct", "local", []string{"calculator"}, true},
		{"service.storageInventory.recentFiles", "service.storageInventory.usageHistory", "local", []string{"recent.json"}, true},
		{"service.storageInventory.auditMarks", "service.storageInventory.workProduct", "config", []string{"annotations"}, true},
		{"service.storageInventory.layoutAndPreferences", "service.storageInventory.userSettings", "config", []string{"layouts", "frozen-panes", "settings", "row-views", "named-views"}, true},
		{"service.storageInventory.analysisAndPlans", "service.storageInventory.workProduct", "config", []string{"workspaces", "recipes", "compositions", "snapshots"}, true},
		{"service.storageInventory.editHistoryAndOutputs", "service.storageInventory.workProduct", "config", []string{"edit-documents", "edit-revisions", "edit-files"}, true},
		{"service.storageInventory.taskAndPageRecovery", "service.storageInventory.workProduct", "config", []string{"task-checkpoints", "recovery", "source-locations", "task-portability"}, true},
		{"service.storageInventory.logs", "service.storageInventory.diagnostics", "config", []string{"logs"}, false},
	}
	if !sameDirectory(r.Local, r.Config) {
		rows = append(rows, group{"service.storageInventory.legacyMarksAndLayout", "service.storageInventory.legacyWorkProduct", "local", []string{"annotations", "layouts"}, true})
	}
	return rows
}

func sameDirectory(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	x, e1 := os.Stat(a)
	y, e2 := os.Stat(b)
	return e1 == nil && e2 == nil && os.SameFile(x, y)
}

func (r Roots) directory(base string) string {
	if base == "config" {
		return r.Config
	}
	return r.Local
}

type Usage struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Paths     []string `json:"paths"`
	Bytes     int64    `json:"bytes"`
	Allocated int64    `json:"allocated"`
	Files     int      `json:"files"`
	Error     string   `json:"error,omitempty"`
}

func openDirectory(name string) (*os.Root, error) {
	st, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, i18n.Error("diagnostic.localstore.catalog.directoryNotRegularDirectory", i18n.Args{"name": name})
	}
	return os.OpenRoot(name)
}

// Only a missing group before traversal means zero. A failure while reading
// an existing tree makes that measurement or backup incomplete.
func (r Roots) walk(ctx context.Context, g group, forBackup bool, visit func(*os.Root, string, fs.FileInfo) error) error {
	root, err := openDirectory(r.directory(g.base))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	for _, part := range g.parts {
		if _, err := root.Lstat(part); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		err := fs.WalkDir(root.FS(), part, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if part == formats.DatabaseSnapshotDirectory && entry.IsDir() && snapshotStageName(entry.Name()) {
				return fs.SkipDir
			}
			if forBackup && name != part {
				base := entry.Name()
				transient := base == ".lease" || base == ".store.lock" || strings.HasPrefix(base, ".") && strings.HasSuffix(base, ".tmp")
				otherUpload := part == "temp" && (entry.IsDir() || !strings.HasPrefix(base, "ExcelLens-upload-"))
				if transient || otherUpload {
					if entry.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
			}
			if entry.IsDir() {
				return nil
			}
			st, err := entry.Info()
			if err != nil {
				return err
			}
			if !st.Mode().IsRegular() {
				return i18n.Error("diagnostic.localstore.catalog.unableVerifyUnconventionalFiles", i18n.Args{"name": name})
			}
			return visit(root, name, st)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r Roots) Inventory(ctx context.Context, locale i18n.Locale) []Usage {
	var rows []Usage
	for _, g := range r.groups() {
		u := Usage{Name: locale.Text(g.name), Kind: locale.Text(g.kind)}
		for _, p := range g.parts {
			u.Paths = append(u.Paths, filepath.Join(r.directory(g.base), p))
		}
		err := r.walk(ctx, g, false, func(root *os.Root, name string, st fs.FileInfo) error {
			size, err := allocatedSize(root, filepath.FromSlash(name), st)
			if err != nil {
				return err
			}
			u.Files++
			u.Bytes += st.Size()
			u.Allocated += size
			return nil
		})
		if err != nil {
			u.Error = locale.ErrorText(err)
		}
		rows = append(rows, u)
	}
	stagePath := filepath.Join(r.Local, formats.DatabaseSnapshotDirectory)
	stages, err := r.SnapshotStages(ctx, false)
	usage := Usage{Name: locale.Text("service.storageInventory.snapshotStaging"), Kind: locale.Text("service.storageInventory.staging"), Paths: []string{stagePath}}
	for _, stage := range stages {
		usage.Bytes += stage.Bytes
		usage.Allocated += stage.allocated
		usage.Files += stage.Files
		if stage.Error != "" {
			err = errors.Join(err, errors.New(stage.Error))
		}
	}
	if err != nil {
		usage.Error = locale.ErrorText(err)
	}
	return append(rows, usage)
}

func (r Roots) backupPart(name string) bool {
	base, rest, ok := strings.Cut(name, "/")
	if !ok || !fs.ValidPath(name) || !filepath.IsLocal(filepath.FromSlash(name)) || strings.ContainsAny(name, "\\:") {
		return false
	}
	part, _, _ := strings.Cut(rest, "/")
	if part == formats.DatabaseSnapshotDirectory {
		_, tail, _ := strings.Cut(rest, "/")
		name, _, _ := strings.Cut(tail, "/")
		if snapshotStageName(name) {
			return false
		}
	}
	for _, g := range r.groups() {
		if g.backup && base == g.base {
			for _, p := range g.parts {
				if p == part && (part != "recent.json" || rest == part) && (part != "temp" || path.Dir(rest) == part && strings.HasPrefix(path.Base(rest), "ExcelLens-upload-")) {
					return true
				}
			}
		}
	}
	return false
}
