package workbook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"

	"excellens/internal/fileidentity"
)

type databaseFileVersion struct {
	info   os.FileInfo
	stamp  fileidentity.Stamp
	change int64
	locked bool
}

func databaseVersion(f *os.File, locked bool) (databaseFileVersion, error) {
	v := databaseFileVersion{locked: locked}
	info, err := f.Stat()
	if err != nil {
		return v, err
	}
	pathInfo, err := os.Lstat(f.Name())
	if err != nil || !sameDatabasePart(info, pathInfo) {
		return v, errors.New("快照文件已更换或不是普通文件")
	}
	v.info = info
	if locked {
		v.stamp, _ = fileidentity.Revision(f, info)
	}
	if v.stamp.Revision == 0 {
		v.change = fileidentity.ChangeHint(f, info)
	}
	return v, nil
}

func openDatabasePart(path string) (*os.File, databaseFileVersion, error) {
	f, locked, err := fileidentity.Open(path)
	if err != nil {
		return nil, databaseFileVersion{}, err
	}
	if runtime.GOOS == "windows" && !locked {
		f.Close()
		return nil, databaseFileVersion{}, errors.New("快照文件正被写入或无法取得只读句柄")
	}
	v, err := databaseVersion(f, locked)
	if err != nil {
		f.Close()
		return nil, v, err
	}
	return f, v, nil
}

func (v databaseFileVersion) equal(other databaseFileVersion) bool {
	return v.info != nil && other.info != nil && sameDatabasePart(v.info, other.info) &&
		v.stamp == other.stamp && v.change == other.change
}

type databaseVerifiedPart struct {
	mu       sync.Mutex
	version  databaseFileVersion
	expected databasePart
}

func (part *databaseVerifiedPart) check(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	part.mu.Lock()
	defer part.mu.Unlock()
	f, version, err := openDatabasePart(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if version.info.Size() != part.expected.Bytes {
		return errors.New("文件长度不匹配")
	}
	if part.version.equal(version) {
		return nil
	}
	actual, err := hashDatabasePart(ctx, f, version, nil)
	if err != nil {
		return err
	}
	if actual != part.expected {
		return fmt.Errorf("快照文件 %s：SHA-256 摘要不匹配", path)
	}
	part.version = version
	return nil
}

type databaseSheetParts struct{ rows, index databaseVerifiedPart }

func (s *databaseSheetParts) check(ctx context.Context, rows, index string) error {
	if err := s.rows.check(ctx, rows); err != nil {
		return err
	}
	return s.index.check(ctx, index)
}

func (w *Workbook) loadDatabaseParts(ctx context.Context) (map[string]*databaseSheetParts, error) {
	out := make(map[string]*databaseSheetParts, len(w.Sheets))
	for n, sheet := range w.Sheets {
		rows, index := w.cachePaths(sheet.Path)
		e := w.source.database.manifest.Tables[n].Evidence
		s := &databaseSheetParts{}
		s.rows.expected, s.index.expected = e.Rows, e.Index
		if err := s.check(ctx, rows, index); err != nil {
			return nil, fmt.Errorf("数据库快照 %s 核验失败：%w", sheet.SourceName, err)
		}
		out[sheet.Path] = s
	}
	return out, nil
}

func (w *Workbook) checkDatabaseSheet(ctx context.Context, sheet string) error {
	source := w.source.database
	s := source.parts[sheet]
	if s == nil {
		return errors.New("数据库快照缺少工作表证据")
	}
	if err := source.manifestPart.check(ctx, w.Path); err != nil {
		return err
	}
	rows, index := w.cachePaths(sheet)
	return s.check(ctx, rows, index)
}
