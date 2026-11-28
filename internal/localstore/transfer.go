package localstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"excellens/internal/i18n"
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
func openRegular(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	name = filepath.FromSlash(name)
	for p := name; p != "."; p = filepath.Dir(p) {
		st, err := root.Lstat(p)
		if err != nil {
			return nil, nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, nil, i18n.Error("diagnostic.localstore.transfer.pathContainsLink", i18n.Args{"p": p})
		}
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}

	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, nil, i18n.Error("diagnostic.localstore.transfer.notRegularFile", i18n.Args{"name": name})
	}
	return f, st, nil
}

func digestCopy(ctx context.Context, src io.Reader, dst io.Writer) (fileRecord, error) {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), meteredReader(ctx, src))
	return fileRecord{n, hex.EncodeToString(h.Sum(nil))}, err
}

func readFile(ctx context.Context, root *os.Root, name string, dst io.Writer) (fileRecord, error) {
	f, st, err := openRegular(root, name)
	if err != nil {
		return fileRecord{}, err
	}
	defer f.Close()
	rec, err := digestCopy(ctx, f, dst)
	if err == nil && rec.Bytes != st.Size() {
		err = i18n.Error("diagnostic.localstore.transfer.fileSizeChanged", i18n.Args{"name": name})
	}
	return rec, err
}

func (r Roots) snapshot(ctx context.Context, dst *backupVolume) (map[string]fileRecord, error) {
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
				f, st, e := openRegular(root, name)
				if e != nil {
					return e
				}
				defer f.Close()
				expected, ok := dst.planned[key]
				if !ok || expected.Bytes != st.Size() {
					return i18n.Error("diagnostic.localstore.transfer.changesOccurredAfterBackupBudget", i18n.Args{"name": name})
				}
				hash := sha256.New()
				count := &countWriter{}
				err = dst.write(key, io.TeeReader(meteredReader(ctx, io.LimitReader(f, st.Size()+1)), io.MultiWriter(hash, count)))
				rec = fileRecord{count.n, hex.EncodeToString(hash.Sum(nil))}
				if err == nil && count.n != st.Size() {
					err = i18n.Error("diagnostic.localstore.transfer.fileSizeChanged", i18n.Args{"name": name})
				}
			}
			if err == nil {
				files[key] = rec
				finishFile(ctx)
			}
			return err
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func (r Roots) externalFolder(folder string) (string, error) {
	if !filepath.IsAbs(folder) {
		return "", i18n.Error("diagnostic.localstore.transfer.selectAbsolutePathExistingDirectory")
	}
	real, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return "", i18n.Error("diagnostic.localstore.transfer.catalogUnavailable")
	}
	for p := real; ; p = filepath.Dir(p) {
		if sameDirectory(p, r.Config) || sameDirectory(p, r.Local) {
			return "", i18n.Error("diagnostic.localstore.transfer.selectLocationOtherThanExcellens")
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
	return r.BackupWith(ctx, parent, TransferOptions{})
}

func (r Roots) BackupWith(ctx context.Context, parent string, options TransferOptions) (published string, err error) {
	ctx = options.start(ctx)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	parent, err = r.externalFolder(parent)
	if err != nil {
		return "", err
	}
	if err := r.AdoptFrozen(ctx); err != nil {
		return "", err
	}
	phase(ctx, "stage.task.budgetDiskSpace", 0, 0)
	space, total, items, err := r.backupSpace(ctx, parent, options)
	if err != nil {
		return "", err
	}
	stage, cleanup, err := newBackupStage(parent)
	if err != nil {
		return "", err
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			if published != "" {
				err = errors.Join(err, i18n.Wrap("diagnostic.localstore.transfer.backupPublishedCleaningCacheFailed", cleanupErr, i18n.Args{"published": published}))
			} else {
				err = errors.Join(err, i18n.Wrap("diagnostic.localstore.transfer.failedCleanUpBackupCache", cleanupErr, i18n.Args{"stage": stage}))
			}
		}
	}()
	payload := filepath.Join(stage, stagePayload)
	dest, err := openDirectory(payload)
	if err != nil {
		return "", err
	}
	defer dest.Close()
	volume, err := newVolume(dest, options.Password, true)
	if err != nil {
		return "", err
	}
	defer clear(volume.key)
	volume.disk, volume.planned = space.targets["backup"], space.planned
	phase(ctx, "stage.task.copying", total, items)
	files, err := r.snapshot(ctx, &volume)
	if err != nil {
		return "", err
	}
	if len(files) != items {
		return "", i18n.Error("diagnostic.localstore.transfer.changesOccurredFileCollectionAfter")
	}
	total = 0
	for _, f := range files {
		total += f.Bytes
	}
	phase(ctx, "stage.task.reviewSource", total, len(files))
	again, err := r.snapshot(ctx, nil)
	if err != nil {
		return "", err
	}
	if !maps.Equal(files, again) {
		return "", i18n.Error("diagnostic.localstore.transfer.fileChangedDuringBackupTry")
	}
	phase(ctx, "stage.task.verifyBackup", total, len(files))
	for name, want := range files {
		if err := volume.verify(ctx, name, want, io.Discard); err != nil {
			return "", err
		}
		finishFile(ctx)
	}
	data, err := json.MarshalIndent(manifest{1, files}, "", "  ")
	if err != nil {
		return "", err
	}
	if len(data) > manifestLimit {
		return "", i18n.Error("diagnostic.localstore.transfer.backupListExceedsCapacityLimit")
	}
	if err = volume.write("manifest.json", bytes.NewReader(data)); err != nil {
		return "", err
	}
	check, err := volume.manifest()
	if err != nil {
		return "", err
	}
	if !bytes.Equal(data, check) {
		return "", i18n.Error("diagnostic.localstore.transfer.backupListReviewFailed")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = syncTree(dest.Name()); err != nil {
		return "", err
	}
	if err = dest.Close(); err != nil {
		return "", err
	}
	out := filepath.Join(parent, strings.TrimPrefix(filepath.Base(stage), "."))
	if _, err = os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		return "", i18n.Error("diagnostic.localstore.transfer.backupTargetAlreadyExistsOr")
	}
	if err = space.check(); err != nil {
		return "", err
	}
	if err = options.enterCommit(ctx); err != nil {
		return "", err
	}
	if err = publishNew(payload, out); err != nil {
		return "", err
	}
	return out, syncDir(parent)
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
		return i18n.Error("diagnostic.localstore.transfer.targetCategoryNotRegularDirectory", i18n.Args{"parent": parent})
	}
	return nil
}
