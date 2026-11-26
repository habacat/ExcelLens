// Package localstore inventories application-owned data and transfers user files.
package localstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"excellens/internal/formats"
)

func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return "", errors.New("无法定位用户配置目录")
	}
	return filepath.Join(base, "ExcelLens"), nil
}

type Roots struct {
	Local  string `json:"local"`
	Config string `json:"config"`
}

type group struct {
	name, kind, base string
	parts            []string
	backup           bool
}

func (r Roots) groups() []group {
	rows := []group{
		{"工作簿索引", "可重建缓存", "local", []string{"cache"}, false},
		{"条件统计缓存", "可重建缓存", "local", []string{formats.ConditionalStatsDirectory}, false},
		{"数据库快照", "审计来源", "local", []string{formats.DatabaseSnapshotDirectory}, true},
		{"上传副本", "来源副本", "local", []string{"temp"}, true},
		{"导出文件", "工作成果", "local", []string{"exports"}, true},
		{"计算历史", "工作成果", "local", []string{"calculator"}, true},
		{"最近文件", "使用记录", "local", []string{"recent.json"}, true},
		{"审计标记", "工作成果", "config", []string{"annotations"}, true},
		{"布局与偏好", "用户设置", "config", []string{"layouts", "frozen-panes", "settings", "row-views", "named-views"}, true},
		{"分析表与核查方案", "工作成果", "config", []string{"workspaces", "recipes", "compositions", "snapshots"}, true},
		{"编辑历史与生成副本", "工作成果", "config", []string{"edit-documents", "edit-revisions", "edit-files"}, true},
		{"任务与页面恢复", "工作成果", "config", []string{"task-checkpoints", "recovery", "source-locations", "task-portability"}, true},
		{"运行日志", "诊断记录", "config", []string{"logs"}, false},
	}
	if !sameDirectory(r.Local, r.Config) {
		rows = append(rows, group{"旧版标记与布局", "旧版工作成果", "local", []string{"annotations", "layouts"}, true})
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
		return nil, fmt.Errorf("目录不是普通目录：%s", name)
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
				return fmt.Errorf("无法核对非常规文件：%s", name)
			}
			return visit(root, name, st)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r Roots) Inventory(ctx context.Context) []Usage {
	var rows []Usage
	for _, g := range r.groups() {
		u := Usage{Name: g.name, Kind: g.kind}
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
			u.Error = err.Error()
		}
		rows = append(rows, u)
	}
	stagePath := filepath.Join(r.Local, formats.DatabaseSnapshotDirectory)
	stages, err := r.SnapshotStages(ctx, false)
	usage := Usage{Name: "数据库快照暂存", Kind: "采集暂存", Paths: []string{stagePath}}
	for _, stage := range stages {
		usage.Bytes += stage.Bytes
		usage.Allocated += stage.allocated
		usage.Files += stage.Files
		if stage.Error != "" {
			err = errors.Join(err, errors.New(stage.Error))
		}
	}
	if err != nil {
		usage.Error = err.Error()
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
