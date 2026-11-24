package localstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Each stage is on its target volume. The durable decision and the ordered list
// of units live under Config. Recovery does not require the backup or password.
const journalName = ".restore-transaction.json"

type restorePlan struct {
	ID    string                `json:"id"`
	Phase string                `json:"phase"`
	Files map[string]fileRecord `json:"files,omitempty"`
}
type restoreUnit struct {
	key, base, rel string
	names          []string
}

func (r Roots) units(files map[string]fileRecord) ([]restoreUnit, error) {
	groups := map[string][]string{}
	physical := map[string]bool{}
	shared := sameDirectory(r.Config, r.Local)
	for name, rec := range files {
		digest, err := hex.DecodeString(rec.SHA256)
		if !r.backupPart(name) || err != nil || len(digest) != 32 || rec.Bytes < 0 || rec.Bytes == math.MaxInt64 {
			return nil, fmt.Errorf("备份含无效记录：%s", name)
		}
		parts := strings.Split(name, "/")
		if len(parts) < 3 && parts[1] != "recent.json" {
			return nil, errors.New("备份文件位置无效")
		}
		key := strings.Join(parts[:min(3, len(parts))], "/")
		groups[key] = append(groups[key], name)
	}
	result := make([]restoreUnit, 0, len(groups))
	for _, key := range slices.Sorted(maps.Keys(groups)) {
		base, rel, _ := strings.Cut(key, "/")
		target := key
		if shared {
			target = rel
		}
		if runtime.GOOS == "windows" {
			target = strings.ToLower(target)
		}
		if physical[target] {
			return nil, fmt.Errorf("重复迁入目标：%s", key)
		}
		physical[target] = true
		result = append(result, restoreUnit{key, base, filepath.FromSlash(rel), groups[key]})
	}
	return result, nil
}

func (r Roots) stage(id, base string) string {
	return filepath.Join(r.directory(base), ".restore-"+id+"-"+base)
}
func (r Roots) savePlan(p restorePlan) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(data) > manifestLimit {
		return errors.New("恢复事务超过容量")
	}
	return replaceDurable(r.Config, journalName, data)
}
func replaceDurable(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, ".restore-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = replaceFile(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}
func (r Roots) cleanupRestore(p restorePlan) error {
	for _, base := range []string{"config", "local"} {
		if err := os.RemoveAll(r.stage(p.ID, base)); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(r.Config, journalName)); err != nil {
		return err
	}
	return syncDir(r.Config)
}

// Recover must run under exclusive application ownership before opening any
// workbook. A failure preserves the decision and blocks use of partial data.
func (r Roots) Recover(ctx context.Context) error {
	return r.RecoverWith(ctx, TransferOptions{})
}

func (r Roots) RecoverWith(ctx context.Context, options TransferOptions) error {
	ctx = options.start(ctx)
	config, err := openDirectory(r.Config)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer config.Close()
	data, err := readBounded(config, journalName, manifestLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var p restorePlan
	if err = json.Unmarshal(data, &p); err != nil {
		return err
	}
	id, err := hex.DecodeString(p.ID)
	if err != nil || len(id) != 16 {
		return errors.New("恢复事务标识无效")
	}
	switch p.Phase {
	case "prepare", "cleanup":
		return r.cleanupRestore(p)
	case "commit":
		if len(p.Files) == 0 {
			return errors.New("恢复事务缺少数据单元")
		}
		if err := options.enterCommit(ctx); err != nil {
			return err
		}
		return r.publishPlan(context.WithoutCancel(ctx), p, true)
	default:
		return errors.New("恢复事务状态无效")
	}
}
func (r Roots) publishPlan(ctx context.Context, p restorePlan, replay bool) error {
	units, err := r.units(p.Files)
	if err != nil {
		return err
	}
	stageConfig := r.stage(p.ID, "config")
	var next int
	stage, err := openDirectory(stageConfig)
	if err != nil {
		return err
	}
	progress, err := readBounded(stage, ".next", 32)
	stage.Close()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (json.Unmarshal(progress, &next) != nil || next < 0 || next > len(units)) {
		return errors.New("恢复事务游标无效")
	}
	var total int64
	if replay {
		for _, rec := range p.Files {
			if rec.Bytes > math.MaxInt64-total {
				return errors.New("恢复事务字节总数无效")
			}
			total += rec.Bytes
		}
	}
	phase(ctx, "提交恢复事务", total, len(units))
	for i, u := range units {
		if i >= next || replay {
			if err := r.installUnit(ctx, p, u, replay, i < next); err != nil {
				return fmt.Errorf("恢复事务等待继续（%s）：%w", u.key, err)
			}
		}
		if i >= next {
			b, _ := json.Marshal(i + 1)
			if err := replaceDurable(stageConfig, ".next", b); err != nil {
				return err
			}
		}
		finishFile(ctx)
	}
	p.Phase, p.Files = "cleanup", nil
	if err := r.savePlan(p); err != nil {
		return err
	}
	return r.cleanupRestore(p)
}
func (r Roots) installUnit(ctx context.Context, p restorePlan, u restoreUnit, replay, acknowledged bool) error {
	dest, err := openDirectory(r.directory(u.base))
	if err != nil {
		return err
	}
	defer dest.Close()
	if err := checkParents(dest, u.rel); err != nil {
		return err
	}
	verify := func(root *os.Root) error {
		for _, name := range u.names {
			rec, err := readFile(ctx, root, strings.TrimPrefix(name, u.base+"/"), io.Discard)
			if err != nil {
				return err
			}
			if rec != p.Files[name] {
				return errors.New("数据与事务清单不符")
			}
		}
		return nil
	}
	if _, err := dest.Lstat(u.rel); err == nil {
		if !replay {
			return os.ErrExist
		}
		return verify(dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if acknowledged {
		return errors.New("已确认迁入的数据缺失")
	}
	stage := r.stage(p.ID, u.base)
	if replay {
		src, err := openDirectory(stage)
		if err != nil {
			return err
		}
		err = verify(src)
		src.Close()
		if err != nil {
			return err
		}
	}
	if err := dest.MkdirAll(filepath.Dir(u.rel), 0700); err != nil {
		return err
	}
	if err := syncDir(dest.Name()); err != nil {
		return err
	}
	target := filepath.Join(dest.Name(), u.rel)
	if err := publishNew(filepath.Join(stage, u.rel), target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

func (r Roots) Pending() bool {
	_, err := os.Lstat(filepath.Join(r.Config, journalName))
	return !errors.Is(err, os.ErrNotExist)
}

// RecoveryPending checks directory and journal metadata only. Parsing the
// manifest and verifying published files belongs to the background replay.
func (r Roots) RecoveryPending() (bool, error) {
	dir, err := openDirectory(r.Config)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer dir.Close()
	_, err = dir.Lstat(journalName)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return true, err
}
