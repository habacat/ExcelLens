package workbook

import (
	"excellens/internal/fileidentity"

	"fmt"
	"strings"
)

type sqliteTable struct {
	Name string
	Kind string
}

func openSQLiteWorkbook(filePath string) (*Workbook, error) {
	fingerprint, st, err := fileidentity.Fingerprint(filePath, "sqlite")
	if err != nil {
		return nil, err
	}
	tables, err := sqliteDiscover(filePath)
	if err != nil {
		return nil, fmt.Errorf("无法只读打开 SQLite 数据库：%w", err)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("SQLite 数据库中没有可浏览的数据表或视图")
	}
	wb, err := newExternalWorkbook(filePath, "sqlite", fingerprint, st)
	if err != nil {
		return nil, err
	}
	wb.SourceInfo = SourceInfo{
		Format: "sqlite", ReadOnly: true, Delimiter: "SQLite 表 / 视图",
		Notice: "只读打开；每张表的首行是字段名称。NULL 以 [NULL] 显示，BLOB 仅显示字节数；源数据库不会修改。若源库使用 WAL 日志，需先在原系统完成检查点后再以浏览器上传单个 DB 文件。",
	}
	wb.source.sqliteNames = make(map[string]string, len(tables))
	names := make(map[string]bool)
	for i, t := range tables {
		// Preserve actual names where possible. An export to XLSX requires
		// sheet names ≤31 characters and forbids []:*?/\\ characters.
		name := safeExternalSheetName(t.Name, i, names)
		meta := SheetMeta{Name: name, Path: fmt.Sprintf("external/sqlite/%d", i)}
		if t.Kind == "view" {
			meta.TabColor = "#a389cc"
		}
		wb.Sheets = append(wb.Sheets, meta)
		wb.source.sqliteNames[meta.Path] = t.Name
	}
	return wb, nil
}
func safeExternalSheetName(name string, i int, seen map[string]bool) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '[', ']', ':', '*', '?', '/', '\\':
			return '_'
		default:
			return r
		}
	}, name)
	name = strings.TrimSpace(strings.Trim(name, "'"))
	if name == "" {
		name = fmt.Sprintf("表%d", i+1)
	}
	r := []rune(name)
	if len(r) > 31 {
		name = string(r[:31])
	}
	original := name
	for j := 2; seen[strings.ToLower(name)]; j++ {
		suffix := fmt.Sprintf("_%d", j)
		r = []rune(original)
		if len(r)+len([]rune(suffix)) > 31 {
			r = r[:31-len([]rune(suffix))]
		}
		name = string(r) + suffix
	}
	seen[strings.ToLower(name)] = true
	return name
}
