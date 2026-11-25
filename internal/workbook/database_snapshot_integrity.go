package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"excellens/internal/fileidentity"
	"excellens/internal/formats"
)

type DatabaseSnapshotSummary struct {
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Taken     time.Time `json:"taken"`
	Sheets    int       `json:"sheets"`
	Records   int64     `json:"records"`
	Bytes     int64     `json:"bytes"`
	Retained  bool      `json:"retained"`
	LegacyKey string    `json:"-"`
	Error     string    `json:"error,omitempty"`
}

func ListDatabaseSnapshots(ctx context.Context) ([]DatabaseSnapshotSummary, error) {
	retained, err := listDatabaseSnapshotRoot(ctx, DatabaseSnapshotRoot(), true)
	if err != nil {
		return nil, err
	}
	legacy, err := listDatabaseSnapshotRoot(ctx, CacheRoot(), false)
	if err != nil {
		return nil, err
	}
	claimed := make(map[string]bool, len(retained))
	for _, item := range retained {
		if item.LegacyKey != "" {
			claimed[item.LegacyKey] = true
		}
	}
	for _, item := range legacy {
		if !claimed[item.Key] {
			retained = append(retained, item)
		}
	}
	slices.SortStableFunc(retained, func(a, b DatabaseSnapshotSummary) int { return b.Taken.Compare(a.Taken) })
	return retained, nil
}

func listDatabaseSnapshotRoot(ctx context.Context, root string, retained bool) ([]DatabaseSnapshotSummary, error) {
	if st, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return []DatabaseSnapshotSummary{}, nil
	} else if err != nil || !st.IsDir() {
		return nil, errors.New("无法读取数据库快照目录")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	out := make([]DatabaseSnapshotSummary, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !databaseDigest(entry.Name()) {
			continue
		}
		key := entry.Name()
		dir := filepath.Join(root, key)
		if !retained {
			has, err := HasLegacyDatabaseSnapshot(dir)
			if err != nil {
				return nil, err
			}
			if !has {
				continue
			}
		}
		data, _, _, err := databaseManifestAt(filepath.Join(dir, databaseSnapshotFile))
		if err != nil {
			out = append(out, DatabaseSnapshotSummary{Key: key, Name: "数据库快照", Error: err.Error()})
			continue
		}
		if retained != (data.Version == 2) {
			out = append(out, DatabaseSnapshotSummary{Key: key, Name: data.Label, Error: "快照的存放位置无效"})
			continue
		}
		item := DatabaseSnapshotSummary{Key: key, Name: data.Label, Taken: data.Taken, Sheets: len(data.Tables), LegacyKey: data.LegacyKey, Retained: retained}
		if retained {
			for _, t := range data.Tables {
				item.Records += int64(t.Evidence.Records)
				item.Bytes += t.Evidence.Rows.Bytes + t.Evidence.Index.Bytes
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (w *Workbook) DatabaseSnapshotEvidence() json.RawMessage {
	if w.Format != "db-online" || w.source.database.manifest.Version != 2 {
		return nil
	}
	data, _ := json.Marshal(w.source.database.manifest)
	return data
}

func HasLegacyDatabaseSnapshot(dir string) (bool, error) {
	_, err := os.Lstat(filepath.Join(dir, databaseSnapshotFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func OpenDatabaseSnapshot(ctx context.Context, path string) (*Workbook, error) {
	return loadDatabaseSnapshot(ctx, path, true)
}

func openDatabaseSnapshot(path string) (*Workbook, error) {
	return OpenDatabaseSnapshot(context.Background(), path)
}

func loadDatabaseSnapshot(ctx context.Context, path string, verify bool) (*Workbook, error) {
	meta, data, st, err := databaseManifestAt(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if meta.Version == 2 {
		dir = filepath.Dir(DatabaseSnapshotPath(databaseSnapshotKey(data)))
	}
	w := databaseWorkbook(meta, databaseSnapshotKey(data), dir)
	w.source.size, w.source.modified = st.Size(), st.ModTime()
	if meta.Version == 2 {
		w.source.database.parts, err = w.loadDatabaseParts(ctx, verify)
		if err != nil {
			return nil, err
		}
	} else {
		for _, sheet := range w.Sheets {
			if w.GetCachedIndex(sheet.Path) == nil {
				return nil, errors.New("旧版数据库快照缺少可读取的行文件或索引")
			}
		}
	}
	// The ordinary workbook cache holds only derived queries. The domain also
	// separates legacy sources whose original parts still reside in CacheRoot.
	cacheKey := databaseSnapshotKey([]byte("database-snapshot-cache-v1\x00" + w.Fingerprint))
	w.CacheDir = filepath.Join(CacheRoot(), cacheKey)
	if err := os.MkdirAll(w.CacheDir, 0700); err != nil {
		return nil, fmt.Errorf("无法创建数据库查询缓存：%w", err)
	}
	return w, nil
}

func databaseManifestAt(path string) (databaseManifest, []byte, os.FileInfo, error) {
	var meta databaseManifest
	if filepath.Base(path) != databaseSnapshotFile {
		return meta, nil, nil, errors.New("数据库快照清单的文件名无效")
	}
	dir := filepath.Dir(path)
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		return meta, nil, nil, errors.New("数据库快照目录不存在或不可访问")
	}
	data, st, err := readDatabaseManifest(path)
	if err != nil {
		return meta, nil, nil, err
	}
	if json.Unmarshal(data, &meta) != nil {
		return meta, nil, nil, errors.New("数据库快照清单损坏")
	}
	if err := validateDatabaseManifest(meta); err != nil {
		return meta, nil, nil, err
	}
	key := databaseSnapshotKey(data)
	expected := DatabaseSnapshotPath(key)
	if meta.Version == 1 {
		expected = LegacyDatabaseSnapshotPath(key)
	}
	expectedInfo, err := os.Lstat(expected)
	if err != nil || !expectedInfo.Mode().IsRegular() || !os.SameFile(expectedInfo, st) ||
		filepath.Base(dir) != key {
		return meta, nil, nil, errors.New("数据库快照的清单摘要或保存位置不匹配")
	}
	return meta, data, st, nil
}

func readDatabaseManifest(path string) ([]byte, os.FileInfo, error) {
	initial, err := os.Lstat(path)
	if err != nil || !initial.Mode().IsRegular() || initial.Size() > formats.DatabaseManifestBytes {
		return nil, nil, errors.New("数据库快照清单缺失、超限或不可读")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, initial.Size()+1))
	current, statErr := os.Lstat(path)
	opened, openErr := f.Stat()
	if err != nil || statErr != nil || openErr != nil || !sameDatabasePart(initial, opened) ||
		!sameDatabasePart(initial, current) || int64(len(data)) != initial.Size() {
		return nil, nil, errors.New("数据库快照清单在读取时发生变化")
	}
	return data, initial, nil
}

func (w *Workbook) checkLegacyDatabaseManifest() error {
	data, _, err := readDatabaseManifest(w.Path)
	if err != nil || databaseSnapshotKey(data) != w.Fingerprint {
		return errors.New("旧版数据库快照清单发生变化")
	}
	return nil
}

// VerifyDatabaseSnapshot checks all part digests against the content-addressed
// manifest. File-identity memoization applies only to reliable revisions.
func (w *Workbook) VerifyDatabaseSnapshot(ctx context.Context) error {
	if w.Format != "db-online" || w.source.database.manifest.Version != 2 {
		return errors.New("此旧版快照没有采集时的内容摘要")
	}
	data, _, err := readDatabaseManifest(w.Path)
	if err != nil || databaseSnapshotKey(data) != w.Fingerprint {
		return errors.New("数据库快照清单摘要不匹配")
	}
	_, err = w.loadDatabaseParts(ctx, true)
	return err
}

func (w *Workbook) loadDatabaseParts(ctx context.Context, verify bool) (map[string][2]os.FileInfo, error) {
	out := make(map[string][2]os.FileInfo, len(w.Sheets))
	for n, sheet := range w.Sheets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rows, index := w.cachePaths(sheet.Path)
		evidence := w.source.database.manifest.Tables[n].Evidence
		var stats [2]os.FileInfo
		for i, item := range []struct {
			path, label string
			part        databasePart
		}{{rows, "行文件", evidence.Rows}, {index, "索引", evidence.Index}} {
			st, err := os.Lstat(item.path)
			if err == nil && (!st.Mode().IsRegular() || st.Size() != item.part.Bytes) {
				err = errors.New("文件类型或长度不匹配")
			}
			if err == nil && verify {
				var actual databasePart
				actual, err = readDatabasePart(ctx, item.path, st, nil)
				if err == nil && actual != item.part {
					err = errors.New("SHA-256 摘要不匹配")
				}
			}
			if err != nil {
				return nil, fmt.Errorf("数据库快照 %s 的%s校验失败：%w", sheet.SourceName, item.label, err)
			}
			stats[i] = st
		}
		out[sheet.Path] = stats
	}
	return out, nil
}

func (w *Workbook) databasePartsUnchanged(sheet string) bool {
	source := w.source.database
	if source.manifest.Version == 1 {
		return true
	}
	expected, ok := source.parts[sheet]
	if !ok {
		return false
	}
	rows, index := w.cachePaths(sheet)
	for i, path := range []string{rows, index} {
		st, err := os.Lstat(path)
		if err != nil || !sameDatabasePart(expected[i], st) {
			return false
		}
	}
	return true
}

func sameDatabasePart(a, b os.FileInfo) bool {
	return b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

const databasePartMemo = "database-snapshot-part-sha256-v1"

type databaseReadContext struct {
	ctx context.Context
	r   io.Reader
}

func (r databaseReadContext) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func readDatabasePart(ctx context.Context, path string, original os.FileInfo, copyTo io.Writer) (databasePart, error) {
	var part databasePart
	f, stable, err := fileidentity.Open(path)
	if err != nil {
		return part, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !sameDatabasePart(original, st) || st.Size() <= 0 || st.Size() > formats.DatabaseSnapshotBytes {
		return part, errors.New("快照文件身份或大小已改变")
	}
	part.Bytes = st.Size()
	var stamp fileidentity.Stamp
	var revision bool
	if stable {
		stamp, revision = fileidentity.Revision(f, st)
	}
	if revision && copyTo == nil {
		part.SHA256, _ = fileidentity.Recall(stamp, databasePartMemo)
	}
	if part.SHA256 == "" {
		h := sha256.New()
		var out io.Writer = h
		if copyTo != nil {
			out = io.MultiWriter(copyTo, h)
		}
		n, err := io.CopyBuffer(out, databaseReadContext{ctx, io.LimitReader(f, part.Bytes+1)}, make([]byte, 1<<20))
		if err != nil {
			return part, err
		}
		if n != part.Bytes {
			return part, errors.New("快照文件读取长度不匹配")
		}
		part.SHA256 = hex.EncodeToString(h.Sum(nil))
	}
	current, err := os.Lstat(path)
	if err != nil || !sameDatabasePart(st, current) {
		return part, errors.New("快照文件在核验时发生变化")
	}
	if revision {
		after, ok := fileidentity.Revision(f, current)
		if !ok || after != stamp {
			return part, errors.New("快照文件在核验时发生变化")
		}
		fileidentity.Remember(stamp, databasePartMemo, part.SHA256)
	}
	return part, ctx.Err()
}
