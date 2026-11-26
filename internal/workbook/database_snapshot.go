package workbook

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	"excellens/internal/localstore"
)

const databaseSnapshotFile = "snapshot" + formats.DatabaseSnapshotExtension

type DatabaseSnapshotSource struct {
	Engine   string `json:"engine"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database,omitempty"`
	TLS      string `json:"tls"`
}

type DatabaseColumn struct {
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	Nullable  *bool  `json:"nullable,omitempty"`
	Precision *int64 `json:"precision,omitempty"`
	Scale     *int64 `json:"scale,omitempty"`
}

type DatabaseTableDefinition struct {
	Query    string           `json:"query"`
	Ordering string           `json:"ordering"`
	Columns  []DatabaseColumn `json:"columns"`
}

type databasePart struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type DatabaseSnapshotTable struct {
	Schema   string                 `json:"schema"`
	Name     string                 `json:"name"`
	Kind     string                 `json:"kind"`
	Evidence *databaseTableEvidence `json:"evidence,omitempty"`
}

type databaseTableEvidence struct {
	DatabaseTableDefinition
	StartedAt  time.Time    `json:"startedAt,omitzero"`
	FinishedAt time.Time    `json:"finishedAt,omitzero"`
	Records    int          `json:"records"`
	Rows       databasePart `json:"rows"`
	Index      databasePart `json:"index"`
}

type databaseManifest struct {
	Version     int                     `json:"version"`
	Nonce       string                  `json:"nonce"`
	Label       string                  `json:"label"`
	Taken       time.Time               `json:"taken"`
	FinishedAt  time.Time               `json:"finishedAt,omitzero"`
	Source      *DatabaseSnapshotSource `json:"source,omitempty"`
	Consistency string                  `json:"consistency,omitempty"`
	LegacyKey   string                  `json:"legacyKey,omitempty"`
	Tables      []DatabaseSnapshotTable `json:"tables"`
}

type DatabaseSnapshotInfo struct {
	Retained bool `json:"retained"`
	Legacy   bool `json:"legacy"`
}

type DatabaseCell struct {
	Kind byte
	Raw  string
}

type databaseSource struct {
	manifest     databaseManifest
	manifestPart databaseVerifiedPart
	parts        map[string]*databaseSheetParts
}

// DatabaseSnapshot streams files into an unpublished sibling directory. Once
// complete, its manifest and parts become visible together under the digest.
type DatabaseSnapshot struct {
	workbook  *Workbook
	manifest  databaseManifest
	next      int
	bytes     int64
	committed bool
	cleanup   func() error
}

func DatabaseSnapshotRoot() string {
	return filepath.Join(DataRoot(), formats.DatabaseSnapshotDirectory)
}

func DatabaseSnapshotPath(key string) string {
	if !databaseDigest(key) {
		return ""
	}
	return filepath.Join(DatabaseSnapshotRoot(), key, databaseSnapshotFile)
}

func LegacyDatabaseSnapshotPath(key string) string {
	if !databaseDigest(key) {
		return ""
	}
	return filepath.Join(CacheRoot(), key, databaseSnapshotFile)
}

func StartDatabaseSnapshot(label string, source DatabaseSnapshotSource, tables []DatabaseSnapshotTable) (*DatabaseSnapshot, error) {
	return startDatabaseSnapshot(databaseManifest{Version: 2, Nonce: rand.Text(), Label: label,
		Taken: time.Now().UTC(), Source: &source, Consistency: "per-table", Tables: tables})
}

func startDatabaseSnapshot(meta databaseManifest) (*DatabaseSnapshot, error) {
	if err := validateDatabaseTables(meta); err != nil {
		return nil, err
	}
	root := DatabaseSnapshotRoot()
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(root); err != nil || !st.IsDir() {
		return nil, errors.New("数据库快照目录不是普通目录")
	}
	dir, cleanup, err := localstore.NewSnapshotStage(root)
	if err != nil {
		return nil, err
	}
	return &DatabaseSnapshot{workbook: databaseWorkbook(meta, "", dir), manifest: meta, cleanup: cleanup}, nil
}

func (d *DatabaseSnapshot) Discard() {
	if d.cleanup != nil {
		_ = d.cleanup()
		d.cleanup = nil
	}
}

func (d *DatabaseSnapshot) WriteTable(ctx context.Context,
	stream func(func(DatabaseTableDefinition) error, func([]DatabaseCell) error) error, progress func(int)) error {
	w := d.workbook
	if d.committed || d.next >= len(w.Sheets) {
		return errors.New("数据库快照的表次序无效")
	}
	evidence := databaseTableEvidence{StartedAt: time.Now().UTC()}
	meta := w.Sheets[d.next]
	rowsPath, idxPath := w.cachePaths(meta.Path)
	file, err := os.OpenFile(rowsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	rowHash := sha256.New()
	writer, err := newExternalRowWriterTo(io.MultiWriter(file, rowHash), filepath.Dir(w.Path))
	if err != nil {
		return err
	}
	defer writer.offsets.close()
	var cells []externalCell
	begin := func(def DatabaseTableDefinition) error {
		if cells != nil || len(def.Columns) == 0 || len(def.Columns) > formats.ExcelColumns {
			return errors.New("数据表的列定义无效")
		}
		evidence.DatabaseTableDefinition = def
		cells = make([]externalCell, len(def.Columns))
		for i, col := range def.Columns {
			if len(col.Name) > maxExternalCell || !utf8.ValidString(col.Name) || !utf8.ValidString(col.Type) {
				return errors.New("数据库字段名称或类型编码无效")
			}
			cells[i] = externalCell{Col: i + 1, Type: 't', Raw: col.Name}
		}
		return writer.cells(cells, len(cells))
	}
	if err := stream(begin, func(values []DatabaseCell) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cells == nil || len(values) != len(cells) {
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
	evidence.FinishedAt = time.Now().UTC()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writer.finish(); err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	index := externalSheetIndex(meta, rowsPath, idxPath, writer)
	indexHash := sha256.New()
	if err := writeIndexFile(index, writer.offsets.count, writer.offsets.file, indexHash); err != nil {
		return err
	}
	st, err := os.Stat(idxPath)
	if err != nil {
		return err
	}
	evidence.Records = writer.rows - 1
	evidence.Rows = databasePart{Bytes: writer.count, SHA256: hex.EncodeToString(rowHash.Sum(nil))}
	evidence.Index = databasePart{Bytes: st.Size(), SHA256: hex.EncodeToString(indexHash.Sum(nil))}
	d.bytes += writer.count + st.Size()
	if d.bytes > formats.DatabaseSnapshotBytes {
		return errors.New("选中的数据表超过 8 GB 快照容量，请缩小读取范围")
	}
	d.manifest.Tables[d.next].Evidence = &evidence
	d.next++
	if progress != nil {
		progress(evidence.Records)
	}
	return nil
}

func (d *DatabaseSnapshot) Commit(ctx context.Context) (*Workbook, error) {
	if d.committed || d.next != len(d.workbook.Sheets) {
		return nil, errors.New("数据库快照尚未完整读取")
	}
	d.manifest.FinishedAt = time.Now().UTC()
	if err := validateDatabaseManifest(d.manifest); err != nil {
		return nil, err
	}
	data, err := json.Marshal(d.manifest)
	if err != nil || len(data) > formats.DatabaseManifestBytes {
		return nil, errors.New("数据库快照清单超出容量")
	}
	file, err := os.OpenFile(d.workbook.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return nil, err
	}
	key := databaseSnapshotKey(data)
	target := filepath.Dir(DatabaseSnapshotPath(key))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Seal only the files we just created in the unpublished directory.
	for _, sheet := range d.workbook.Sheets {
		rows, index := d.workbook.cachePaths(sheet.Path)
		for _, path := range []string{rows, index} {
			if err := os.Chmod(path, 0400); err != nil {
				return nil, err
			}
		}
	}
	if err := os.Chmod(d.workbook.Path, 0400); err != nil {
		return nil, err
	}
	st, err := os.Lstat(d.workbook.Path)
	if err != nil {
		return nil, err
	}
	// Detach the published metadata from the writer and caller-owned table slices.
	var sealed databaseManifest
	if err := json.Unmarshal(data, &sealed); err != nil {
		return nil, err
	}
	wb, err := prepareDatabaseWorkbook(ctx, sealed, key, filepath.Dir(d.workbook.Path), st)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(filepath.Dir(d.workbook.Path), target); err != nil {
		return nil, err
	}
	d.committed = true
	d.Discard()
	wb.Path = filepath.Join(target, databaseSnapshotFile)
	return wb, nil
}

func databaseWorkbook(meta databaseManifest, key, dir string) *Workbook {
	w := &Workbook{Format: "db-online", Path: filepath.Join(dir, databaseSnapshotFile), DisplayName: meta.Label,
		Fingerprint: key, NumFmts: make(map[int]string), indexes: make(map[string]*SheetIndex),
		source: &externalSource{format: "db-online", database: &databaseSource{manifest: meta}}}
	legacy := meta.Version == 1 || meta.LegacyKey != ""
	notice := "数据库快照 " + meta.Taken.Local().Format("2006-01-02 15:04:05")
	if meta.Version == 1 {
		notice += "（旧版，未记录内容摘要）"
	} else if legacy {
		notice += "（旧版快照，自保存时起可核验）"
	} else if len(meta.Tables) > 1 && meta.Consistency == "per-table" {
		notice += "（各表独立采集）"
	}
	w.SourceInfo = SourceInfo{Format: w.Format, ReadOnly: true, PresentationNotice: notice,
		DatabaseSnapshot: &DatabaseSnapshotInfo{Retained: meta.Version == 2, Legacy: legacy}}
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

func databaseSnapshotKey(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func databaseDigest(text string) bool {
	if len(text) != sha256.Size*2 {
		return false
	}
	for _, r := range text {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func validateDatabaseTables(meta databaseManifest) error {
	if meta.Nonce == "" || meta.Taken.IsZero() || meta.Label == "" || len(meta.Label) > 1024 ||
		len(meta.Tables) == 0 || len(meta.Tables) > formats.DatabaseSelectedTables {
		return errors.New("数据库快照的来源清单无效")
	}
	for _, table := range meta.Tables {
		name := table.Schema + table.Name
		if table.Schema == "" || table.Name == "" || len(table.Schema) > 512 || len(table.Name) > 512 ||
			!utf8.ValidString(name) || strings.ContainsRune(name, 0) || table.Kind != "table" && table.Kind != "view" {
			return errors.New("数据库快照的表名或架构无效")
		}
	}
	return nil
}

func validateDatabaseManifest(meta databaseManifest) error {
	if err := validateDatabaseTables(meta); err != nil {
		return err
	}
	if meta.Version == 1 {
		return nil
	}
	if meta.Version != 2 || meta.FinishedAt.Before(meta.Taken) {
		return errors.New("数据库快照的清单版本或时间无效")
	}
	if meta.LegacyKey == "" {
		s := meta.Source
		if s == nil || s.Engine == "" || s.Protocol == "" || s.Host == "" || s.Port <= 0 || s.Port > 65535 || meta.Consistency != "per-table" {
			return errors.New("数据库快照的连接来源无效")
		}
	} else if !databaseDigest(meta.LegacyKey) || meta.Source != nil || meta.Consistency != "unknown" {
		return errors.New("旧版数据库快照的来源无效")
	}
	var total int64
	for _, table := range meta.Tables {
		e := table.Evidence
		if e == nil || len(e.Columns) == 0 || len(e.Columns) > formats.ExcelColumns || e.Records < 0 || e.Records >= formats.ExternalRows ||
			e.Query == "" || e.Ordering != "unspecified" {
			return errors.New("数据库快照的字段、查询或记录数无效")
		}
		if meta.LegacyKey == "" && (e.StartedAt.Before(meta.Taken) || e.FinishedAt.Before(e.StartedAt) || e.FinishedAt.After(meta.FinishedAt)) {
			return errors.New("数据库快照的数据读取时间无效")
		}
		for _, part := range []databasePart{e.Rows, e.Index} {
			if part.Bytes <= 0 || part.Bytes > formats.DatabaseSnapshotBytes-total || !databaseDigest(part.SHA256) {
				return errors.New("数据库快照的文件摘要或大小无效")
			}
			total += part.Bytes
		}
	}
	return nil
}

func (w *Workbook) DatabaseSnapshotSize() int64 {
	var size int64
	if w.Format == "db-online" {
		for i, table := range w.source.database.manifest.Tables {
			if table.Evidence != nil {
				size += table.Evidence.Rows.Bytes + table.Evidence.Index.Bytes
			} else if index := w.GetCachedIndex(w.Sheets[i].Path); index != nil {
				size += index.XMLSize
			}
		}
	}
	return size
}
