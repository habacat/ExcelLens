package workbook

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"excellens/internal/formats"
)

const databaseSnapshotFile = "snapshot" + formats.DatabaseSnapshotExtension

type DatabaseSnapshotTable struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
}

type databaseManifest struct {
	Version int                     `json:"version"`
	Nonce   string                  `json:"nonce"`
	Label   string                  `json:"label"`
	Taken   time.Time               `json:"taken"`
	Tables  []DatabaseSnapshotTable `json:"tables"`
}

type DatabaseCell struct {
	Kind byte
	Raw  string
}

// DatabaseSnapshot writes immutable indexed rows. A source can be reopened
// only once the manifest has been committed. The caller pins the directory
// across the write so cache maintenance cannot remove in-progress data.
type DatabaseSnapshot struct {
	workbook  *Workbook
	manifest  []byte
	next      int
	bytes     int64
	committed bool
}

func StartDatabaseSnapshot(label string, tables []DatabaseSnapshotTable) (*DatabaseSnapshot, error) {
	meta := databaseManifest{Version: 1, Nonce: rand.Text(), Label: label, Taken: time.Now().UTC(), Tables: tables}
	if err := validateDatabaseManifest(meta); err != nil {
		return nil, err
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	key := databaseSnapshotKey(data)
	cacheDir := filepath.Join(CacheRoot(), key)
	if err := os.MkdirAll(filepath.Dir(cacheDir), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(cacheDir, 0700); err != nil {
		return nil, err
	}
	w := databaseWorkbook(meta, key, cacheDir)
	return &DatabaseSnapshot{workbook: w, manifest: data}, nil
}

func (d *DatabaseSnapshot) Workbook() *Workbook { return d.workbook }

// Discard removes a private snapshot that has not been installed in a session.
func (d *DatabaseSnapshot) Discard() { _ = os.RemoveAll(d.workbook.CacheDir) }

func (d *DatabaseSnapshot) WriteTable(ctx context.Context, stream func(func([]DatabaseCell) error) error, progress func(int)) error {
	w := d.workbook
	if d.committed || d.next >= len(w.Sheets) {
		return errors.New("数据库快照的表次序无效")
	}
	meta := w.Sheets[d.next]
	rowsPath, idxPath := w.cachePaths(meta.Path)
	file, err := os.OpenFile(rowsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	writer, err := newExternalRowWriter(file)
	if err != nil {
		return err
	}
	defer writer.offsets.close()
	var cells []externalCell
	if err := stream(func(values []DatabaseCell) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cells == nil {
			if len(values) == 0 || len(values) > formats.ExcelColumns {
				return errors.New("数据表的列数无效")
			}
			cells = make([]externalCell, len(values))
		}
		if len(values) != len(cells) {
			return errors.New("读取期间数据表的列数发生变化")
		}
		for i, cell := range values {
			cells[i] = externalCell{Col: i + 1, Type: cell.Kind, Raw: cell.Raw}
		}
		if err := writer.cells(cells, len(cells)); err != nil {
			return err
		}
		if d.bytes+writer.count+24*int64(writer.rows) > formats.DatabaseSnapshotBytes {
			return errors.New("选中的数据表超过 8 GB 快照容量，请缩小读取范围")
		}
		if progress != nil && writer.rows%2048 == 0 {
			progress(writer.rows - 1)
		}
		return nil
	}); err != nil {
		return err
	}
	if writer.rows == 0 {
		return errors.New("数据库未返回数据表字段")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writer.finish(); err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if _, err := w.publishExternalIndex(meta, rowsPath, idxPath, writer); err != nil {
		return err
	}
	d.bytes += writer.count + 24*int64(writer.rows)
	d.next++
	if progress != nil {
		progress(writer.rows - 1)
	}
	return nil
}

func (d *DatabaseSnapshot) Commit() error {
	if d.committed || d.next != len(d.workbook.Sheets) {
		return errors.New("数据库快照尚未完整读取")
	}
	temp, err := os.CreateTemp(d.workbook.CacheDir, ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(d.manifest)
	if err = errors.Join(err, temp.Sync(), temp.Close()); err != nil {
		return err
	}
	info, err := os.Stat(temp.Name())
	if err != nil {
		return err
	}
	if err := replaceFile(temp.Name(), d.workbook.Path); err != nil {
		return err
	}
	d.workbook.source.size, d.workbook.source.modified = info.Size(), info.ModTime()
	d.committed = true
	return nil
}

func openDatabaseSnapshot(file string) (*Workbook, error) {
	data, info, err := readDatabaseManifest(file)
	if err != nil {
		return nil, err
	}
	var meta databaseManifest
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, errors.New("数据库快照清单损坏")
	}
	if err := validateDatabaseManifest(meta); err != nil {
		return nil, err
	}
	key := databaseSnapshotKey(data)
	cacheDir := filepath.Join(CacheRoot(), key)
	if expected, err := os.Stat(filepath.Join(cacheDir, databaseSnapshotFile)); err != nil || !os.SameFile(expected, info) {
		return nil, errors.New("数据库快照不在本机缓存中，请重新连接并读取")
	}
	w := databaseWorkbook(meta, key, cacheDir)
	w.source.size, w.source.modified = info.Size(), info.ModTime()
	for _, sheet := range w.Sheets {
		if w.GetCachedIndex(sheet.Path) == nil {
			return nil, errors.New("数据库快照的行缓存已清除或损坏，请重新连接")
		}
	}
	return w, nil
}

func databaseWorkbook(meta databaseManifest, key, cacheDir string) *Workbook {
	w := &Workbook{Format: "db-online", Path: filepath.Join(cacheDir, databaseSnapshotFile), DisplayName: meta.Label,
		Fingerprint: key, CacheDir: cacheDir, NumFmts: make(map[int]string), indexes: make(map[string]*SheetIndex),
		source: &externalSource{format: "db-online"}}
	w.SourceInfo = SourceInfo{Format: w.Format, ReadOnly: true,
		PresentationNotice: "数据库快照 " + meta.Taken.Local().Format("2006-01-02 15:04:05")}
	seen := make(map[string]bool, len(meta.Tables))
	for i, table := range meta.Tables {
		name := safeExternalSheetName(table.Name, i, seen)
		sheet := SheetMeta{Name: name, Path: fmt.Sprintf("database/%d", i), SourceName: table.Schema + "." + table.Name}
		if table.Kind == "view" {
			sheet.TabColor = "#a389cc"
		}
		w.Sheets = append(w.Sheets, sheet)
	}
	return w
}

func validateDatabaseManifest(meta databaseManifest) error {
	if meta.Version != 1 || meta.Nonce == "" || meta.Taken.IsZero() || meta.Label == "" || len(meta.Label) > 1024 ||
		len(meta.Tables) == 0 || len(meta.Tables) > formats.DatabaseSelectedTables {
		return errors.New("数据库快照的来源清单无效")
	}
	for _, table := range meta.Tables {
		if table.Schema == "" || table.Name == "" || len(table.Schema) > 512 || len(table.Name) > 512 ||
			!utf8.ValidString(table.Schema+table.Name) || strings.ContainsRune(table.Name+table.Schema, 0) || table.Kind != "table" && table.Kind != "view" {
			return errors.New("数据库快照的表名或架构无效")
		}
	}
	return nil
}

func databaseSnapshotKey(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func readDatabaseManifest(file string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > formats.DatabaseRequestBytes {
		return nil, nil, errors.New("数据库快照清单超限或不可读")
	}
	data, err := io.ReadAll(io.LimitReader(f, formats.DatabaseRequestBytes+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, nil, errors.New("数据库快照清单在读取时发生变化")
	}
	return data, info, nil
}

func (w *Workbook) verifyDatabaseSnapshot() error {
	data, _, err := readDatabaseManifest(w.Path)
	if err != nil || databaseSnapshotKey(data) != w.Fingerprint {
		return errors.New("数据库快照发生变化，请重新连接并读取")
	}
	return nil
}

func (w *Workbook) DatabaseSnapshotSize() int64 {
	var size int64
	if w.Format == "db-online" {
		for _, sheet := range w.Sheets {
			if index := w.GetCachedIndex(sheet.Path); index != nil {
				size += index.XMLSize
			}
		}
	}
	return size
}
