package localstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type fileRecord struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type manifest struct {
	Version int                   `json:"version"`
	Files   map[string]fileRecord `json:"files"`
}

type cancelledReader struct {
	ctx context.Context
	io.Reader
}

func (r cancelledReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

// Open relative to the pinned root. Recheck the actual file, even when it was
// already enumerated. Never follow a symlink in an application data group.
func readFile(ctx context.Context, root *os.Root, name string, dst io.Writer) (fileRecord, error) {
	name = filepath.FromSlash(name)
	for p := name; p != "."; p = filepath.Dir(p) {
		st, err := root.Lstat(p)
		if err != nil {
			return fileRecord{}, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fileRecord{}, fmt.Errorf("路径含链接：%s", p)
		}
	}
	f, err := root.Open(name)
	if err != nil {
		return fileRecord{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return fileRecord{}, err
	}
	if !st.Mode().IsRegular() {
		return fileRecord{}, fmt.Errorf("不是普通文件：%s", name)
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), cancelledReader{ctx, f})
	if err == nil && n != st.Size() {
		err = fmt.Errorf("文件大小已变化：%s", name)
	}
	return fileRecord{n, hex.EncodeToString(hash.Sum(nil))}, err
}

func copyFile(ctx context.Context, src *os.Root, name string, dst *os.Root, out string) (fileRecord, error) {
	out = filepath.FromSlash(out)
	if err := dst.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return fileRecord{}, err
	}
	f, err := dst.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fileRecord{}, err
	}
	record, err := readFile(ctx, src, name, f)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	return record, err
}

func (r Roots) snapshot(ctx context.Context, dst *os.Root) (map[string]fileRecord, error) {
	files := make(map[string]fileRecord)
	for _, g := range r.groups() {
		if !g.backup {
			continue
		}
		err := r.walk(ctx, g, true, func(root *os.Root, name string, _ fs.FileInfo) error {
			key := g.base + "/" + name
			var rec fileRecord
			var err error
			if dst == nil {
				rec, err = readFile(ctx, root, name, io.Discard)
			} else {
				rec, err = copyFile(ctx, root, name, dst, key)
			}
			if err == nil {
				files[key] = rec
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func (r Roots) externalFolder(folder string) (string, error) {
	if !filepath.IsAbs(folder) {
		return "", errors.New("请选择已有目录的绝对路径")
	}
	real, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", errors.New("目录不可用")
	}
	for p := real; ; p = filepath.Dir(p) {
		if sameDirectory(p, r.Config) || sameDirectory(p, r.Local) {
			return "", errors.New("请选择 ExcelLens 数据目录以外的位置")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return real, nil
}

// Stage outside both data roots. Publish only after the source file set and
// every source and copied digest have been checked against the first pass.
func (r Roots) Backup(ctx context.Context, parent string) (string, error) {
	parent, err := r.externalFolder(parent)
	if err != nil {
		return "", err
	}
	if err := r.AdoptFrozen(ctx); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".ExcelLens-"+time.Now().Format("20060102-150405")+"-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	dest, err := openDirectory(stage)
	if err != nil {
		return "", err
	}
	defer dest.Close()
	files, err := r.snapshot(ctx, dest)
	if err != nil {
		return "", err
	}
	again, err := r.snapshot(ctx, nil)
	if err != nil {
		return "", err
	}
	if !maps.Equal(files, again) {
		return "", errors.New("备份期间文件发生变化，请待保存完成后重试")
	}
	for name, want := range files {
		got, err := readFile(ctx, dest, name, io.Discard)
		if err != nil {
			return "", err
		}
		if got != want {
			return "", fmt.Errorf("备份校验失败：%s", name)
		}
	}
	data, err := json.MarshalIndent(manifest{1, files}, "", "  ")
	if err != nil {
		return "", err
	}
	if err = dest.WriteFile("manifest.json", data, 0600); err != nil {
		return "", err
	}
	if err = dest.Close(); err != nil {
		return "", err
	}
	out := filepath.Join(parent, strings.TrimPrefix(filepath.Base(stage), "."))
	if _, err = os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("备份目标已存在或不可用")
	}
	if err = publishNew(stage, out); err != nil {
		return "", err
	}
	return out, nil
}

// The caller excludes application writers. Each document or checkpoint directory
// is installed whole. Other documents in the same category can coexist.
func (r Roots) Restore(ctx context.Context, folder string) error {
	folder, err := r.externalFolder(folder)
	if err != nil {
		return err
	}
	src, err := openDirectory(folder)
	if err != nil {
		return err
	}
	defer src.Close()
	data, err := src.ReadFile("manifest.json")
	if err != nil {
		return err
	}
	var m manifest
	if err = json.Unmarshal(data, &m); err != nil || m.Version != 1 || m.Files == nil {
		return errors.New("备份清单无效")
	}
	units := map[string][]string{}
	for name := range m.Files {
		if !r.backupPart(name) {
			return fmt.Errorf("备份含未知分类或路径：%s", name)
		}
		segments := strings.Split(name, "/")
		if len(segments) < 3 && segments[1] != "recent.json" {
			return errors.New("备份文件位置无效")
		}
		unit := strings.Join(segments[:min(3, len(segments))], "/")
		units[unit] = append(units[unit], name)
	}
	type stagedRoot struct{ dest, temp *os.Root }
	stages := map[string]stagedRoot{}
	var pending []string
	destinations := map[string]bool{}
	for _, unit := range slices.Sorted(maps.Keys(units)) {
		base, relUnit, _ := strings.Cut(unit, "/")
		dir := r.directory(base)
		stage, ok := stages[base]
		if !ok {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			dest, err := openDirectory(dir)
			if err != nil {
				return err
			}
			defer dest.Close()
			tmp, err := os.MkdirTemp(dir, ".restore-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)
			temp, err := openDirectory(tmp)
			if err != nil {
				return err
			}
			defer temp.Close()
			stage = stagedRoot{dest, temp}
			stages[base] = stage
		}
		if err := checkParents(stage.dest, relUnit); err != nil {
			return err
		}
		// Config and cache can be the same physical directory in portable profiles.
		key := unit
		if sameDirectory(r.Local, r.Config) {
			key = relUnit
		}
		if destinations[key] {
			return fmt.Errorf("备份有重复迁入目标：%s", key)
		}
		destinations[key] = true
		st, err := stage.dest.Lstat(filepath.FromSlash(relUnit))
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if exists && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("迁入目标含链接：%s", unit)
		}
		for _, name := range units[unit] {
			rel := strings.TrimPrefix(name, base+"/")
			var got fileRecord
			if exists {
				got, err = readFile(ctx, src, name, io.Discard)
			} else {
				got, err = copyFile(ctx, src, name, stage.temp, rel)
			}
			if err != nil {
				return err
			}
			if got != m.Files[name] {
				return fmt.Errorf("备份校验失败：%s", name)
			}
			if exists {
				h, err := readFile(ctx, stage.dest, rel, io.Discard)
				if err != nil || h != got {
					return fmt.Errorf("目标已有不同数据：%s", unit)
				}
			}
		}
		if !exists {
			pending = append(pending, unit)
		}
	}
	for _, unit := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		base, rel, _ := strings.Cut(unit, "/")
		stage := stages[base]
		rel = filepath.FromSlash(rel)
		if err := checkParents(stage.dest, rel); err != nil {
			return err
		}
		if err := stage.dest.MkdirAll(filepath.Dir(rel), 0700); err != nil {
			return err
		}
		if err := publishNew(filepath.Join(stage.temp.Name(), rel), filepath.Join(stage.dest.Name(), rel)); err != nil {
			return err
		}
	}
	return nil
}

// Each restore unit is either a top-level file or one child of a category.
func checkParents(root *os.Root, rel string) error {
	parent := filepath.Dir(filepath.FromSlash(rel))
	if parent == "." {
		return nil
	}
	st, err := root.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("目标分类不是普通目录：%s", parent)
	}
	return nil
}
