package localstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/i18n"
)

func (r Roots) Restore(ctx context.Context, folder string) error {
	return r.RestoreWith(ctx, folder, TransferOptions{})
}
func (r Roots) RestoreWith(ctx context.Context, folder string, options TransferOptions) (err error) {
	if err = r.RecoverWith(ctx, options); err != nil {
		return err
	}
	ctx = options.start(ctx)
	folder, err = r.externalFolder(folder)
	if err != nil {
		return err
	}
	src, err := openDirectory(folder)
	if err != nil {
		return err
	}
	defer src.Close()
	volume, err := newVolume(src, options.Password, false)
	if err != nil {
		return err
	}
	defer clear(volume.key)
	data, err := volume.manifest()
	if err != nil {
		return err
	}
	var m manifest
	if err = json.Unmarshal(data, &m); err != nil || m.Version != 1 || m.Files == nil {
		return i18n.Error("diagnostic.localstore.restore.invalidBackupList")
	}
	units, err := r.units(m.Files)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if len(units) == 0 {
		return nil
	}
	phase(ctx, "stage.task.budgetDiskSpace", 0, len(m.Files))
	space, err := r.restoreSpace(ctx, units, m.Files, options)
	if err != nil {
		return err
	}
	var id [16]byte
	rand.Read(id[:])
	p := restorePlan{ID: hex.EncodeToString(id[:]), Phase: "prepare"}
	if err = os.MkdirAll(r.Config, 0700); err != nil {
		return err
	}
	if err = r.savePlan(p); err != nil {
		return err
	}
	preparing := true
	defer func() {
		if preparing {
			err = errors.Join(err, r.cleanupRestore(p))
		}
	}()
	stages := map[string]*os.Root{}
	for _, base := range []string{"config", "local"} {
		if err = os.MkdirAll(r.directory(base), 0700); err != nil {
			return err
		}
		if err = os.Mkdir(r.stage(p.ID, base), 0700); err != nil {
			return err
		}
		stage, err := openDirectory(r.stage(p.ID, base))
		if err != nil {
			return err
		}
		defer stage.Close()
		stages[base] = stage
	}
	pending := map[string]fileRecord{}
	var total int64
	for _, f := range m.Files {
		if f.Bytes > math.MaxInt64-total {
			return i18n.Error("diagnostic.localstore.restore.backupByteCountInvalid")
		}
		total += f.Bytes
	}
	phase(ctx, "stage.task.verificationAndTemporaryStorage", total, len(m.Files))
	for _, u := range units {
		stage := stages[u.base]
		dest, err := openDirectory(r.directory(u.base))
		if err != nil {
			return err
		}
		err = stageUnit(ctx, volume, m.Files, u, dest, stage, pending, space)
		dest.Close()
		if err != nil {
			return err
		}
	}
	if len(pending) == 0 {
		return nil
	}
	var stagedBytes int64
	for _, record := range pending {
		stagedBytes += record.Bytes
	}
	phase(ctx, "stage.task.reviewAndTentativeStorage", stagedBytes, len(pending))
	for name, want := range pending {
		base, rel, _ := strings.Cut(name, "/")
		got, err := readFile(ctx, stages[base], rel, io.Discard)
		if err != nil {
			return err
		}
		if got != want {
			return i18n.Error("diagnostic.localstore.restore.fileSaveCheckFailed", i18n.Args{"name": name})
		}
		finishFile(ctx)
	}
	for _, s := range stages {
		if err = s.Close(); err != nil {
			return err
		}
		if err = syncTree(s.Name()); err != nil {
			return err
		}
		if err = syncDir(filepath.Dir(s.Name())); err != nil {
			return err
		}
	}
	if err = space.check(); err != nil {
		return err
	}
	if err = options.enterCommit(ctx); err != nil {
		return err
	}
	p.Phase, p.Files = "commit", pending
	preparing = false // A failed durability acknowledgement may already be visible.
	if err = r.savePlan(p); err != nil {
		return err
	}
	return r.publishPlan(context.WithoutCancel(ctx), p, false)
}
func stageUnit(ctx context.Context, volume backupVolume, files map[string]fileRecord, u restoreUnit, dest, stage *os.Root, pending map[string]fileRecord, space *transferSpace) error {
	if err := checkParents(dest, u.rel); err != nil {
		return err
	}
	st, err := dest.Lstat(u.rel)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if exists && st.Mode()&os.ModeSymlink != 0 {
		return i18n.Error("diagnostic.localstore.restore.migrateTargetLink", i18n.Args{"key": u.key})
	}
	for _, name := range u.names {
		rel := strings.TrimPrefix(name, u.base+"/")
		want := files[name]
		if exists {
			if err := volume.verify(ctx, name, want, io.Discard); err != nil {
				return err
			}
			got, err := readFile(withoutMeter(ctx), dest, rel, io.Discard)
			if err != nil || got != want {
				return i18n.Error("diagnostic.localstore.restore.targetAlreadyDifferentData", i18n.Args{"key": u.key})
			}
		} else {
			if _, planned := space.planned[name]; !planned {
				return i18n.Error("diagnostic.localstore.restore.targetChangedAfterSpatialPre", i18n.Args{"key": u.key})
			}
			err := writeNew(stage, rel, func(f *os.File) error {
				if err := volume.verify(ctx, name, want, space.targets[u.base].writer(f)); err != nil {
					return err
				}
				if u.base == "local" && strings.HasPrefix(rel, formats.DatabaseSnapshotDirectory+"/") {
					return f.Chmod(0400)
				}
				return nil
			})
			if err != nil {
				return err
			}
			pending[name] = want
		}
		finishFile(ctx)
	}
	return nil
}
func withoutMeter(ctx context.Context) context.Context {
	return context.WithValue(ctx, meterKey{}, (*meter)(nil))
}
