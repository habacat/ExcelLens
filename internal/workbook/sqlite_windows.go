//go:build windows

package workbook

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// winsqlite3.dll is included with Windows 10 and Windows 11. Use the native
// SQLite C ABI with explicit read-only flags and prepared statements. No CGO,
// third-party drivers, Oracle client, or external executable is required.
var (
	sqlDLL               = syscall.NewLazyDLL("winsqlite3.dll")
	sqlOpen              = sqlDLL.NewProc("sqlite3_open_v2")
	sqlClose             = sqlDLL.NewProc("sqlite3_close")
	sqlPrepare           = sqlDLL.NewProc("sqlite3_prepare_v2")
	sqlStep              = sqlDLL.NewProc("sqlite3_step")
	sqlFinalize          = sqlDLL.NewProc("sqlite3_finalize")
	sqlColCount          = sqlDLL.NewProc("sqlite3_column_count")
	sqlColName           = sqlDLL.NewProc("sqlite3_column_name")
	sqlColType           = sqlDLL.NewProc("sqlite3_column_type")
	sqlColText           = sqlDLL.NewProc("sqlite3_column_text")
	sqlColBytes          = sqlDLL.NewProc("sqlite3_column_bytes")
	sqlErrMsg            = sqlDLL.NewProc("sqlite3_errmsg")
	winGetCurrentProcess = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentProcess")
	winReadProcessMemory = syscall.NewLazyDLL("kernel32.dll").NewProc("ReadProcessMemory")
)

const (
	sqlOK           = 0
	sqlRow          = 100
	sqlDone         = 101
	sqlOpenReadOnly = 0x00000001
	sqlOpenNoMutex  = 0x00008000
	sqlNull         = 5
	sqlBlob         = 4
)

type readonlySQLite struct{ ptr uintptr }

func openReadonlySQLite(path string) (*readonlySQLite, error) {
	if err := sqlDLL.Load(); err != nil {
		return nil, fmt.Errorf("系统缺少 SQLite 组件 winsqlite3.dll：%w", err)
	}
	if err := sqlOpen.Find(); err != nil {
		return nil, fmt.Errorf("系统 SQLite 组件不完整：%w", err)
	}
	if strings.IndexByte(path, 0) >= 0 {
		return nil, errors.New("文件路径包含无效字符")
	}
	name := append([]byte(path), 0)
	var db uintptr
	rc, _, _ := sqlOpen.Call(uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&db)), sqlOpenReadOnly|sqlOpenNoMutex, 0)
	runtime.KeepAlive(name)
	if rc != sqlOK {
		msg := "无法以只读方式打开数据库"
		if db != 0 {
			msg = sqliteLastError(db)
			_, _, _ = sqlClose.Call(db)
		}
		return nil, fmt.Errorf("%s (SQLite 错误 %d)", msg, rc)
	}
	return &readonlySQLite{ptr: db}, nil
}
func (d *readonlySQLite) Close() {
	if d.ptr != 0 {
		_, _, _ = sqlClose.Call(d.ptr)
		d.ptr = 0
	}
}
func sqliteReadLocal(addr uintptr, n int) ([]byte, error) {
	if n < 0 || n > 8<<20 {
		return nil, fmt.Errorf("SQLite 字段或标识符超过 8MB 限制: %d", n)
	}
	if n == 0 {
		return []byte{}, nil
	}
	if addr == 0 {
		return nil, errors.New("SQLite 返回空指针")
	}
	b := make([]byte, n)
	proc, _, _ := winGetCurrentProcess.Call()
	if proc == 0 {
		return nil, errors.New("GetCurrentProcess 失败")
	}
	var copied uintptr
	ok, _, e := winReadProcessMemory.Call(proc, addr, uintptr(unsafe.Pointer(&b[0])), uintptr(n), uintptr(unsafe.Pointer(&copied)))
	runtime.KeepAlive(b)
	if ok == 0 || copied != uintptr(n) {
		return nil, fmt.Errorf("读取 SQLite 数据字段失败（复制 %d/%d）：%v", copied, n, e)
	}
	return b, nil
}
func sqliteCString(ptr uintptr) (string, error) {
	// Column names and sqlite3_errmsg are NUL terminated, with no exposed
	// byte length. Read in bounded chunks; never dereference uintptr in Go.
	var result []byte
	for i := 0; i < 65536; i++ {
		b, e := sqliteReadLocal(ptr+uintptr(i), 1)
		if e != nil {
			return "", e
		}
		if b[0] == 0 {
			return string(result), nil
		}
		result = append(result, b[0])
	}
	return "", errors.New("SQLite 标识符超过 64KB")
}
func sqliteLastError(db uintptr) string {
	p, _, _ := sqlErrMsg.Call(db)
	if p == 0 {
		return "SQLite unknown error"
	}
	s, e := sqliteCString(p)
	if e != nil {
		return e.Error()
	}
	return s
}
func (d *readonlySQLite) prepare(query string) (uintptr, error) {
	if strings.ContainsRune(query, 0) {
		return 0, errors.New("SQL 语句包含 NUL")
	}
	q := append([]byte(query), 0)
	var stmt uintptr
	rc, _, _ := sqlPrepare.Call(d.ptr, uintptr(unsafe.Pointer(&q[0])), ^uintptr(0), uintptr(unsafe.Pointer(&stmt)), 0)
	runtime.KeepAlive(q)
	if rc != sqlOK {
		if stmt != 0 {
			_, _, _ = sqlFinalize.Call(stmt)
		}
		return 0, fmt.Errorf("SQLite 查询失败：%s", sqliteLastError(d.ptr))
	}
	if stmt == 0 {
		return 0, errors.New("SQL 语句为空")
	}
	return stmt, nil
}
func quoteSQLiteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
func (d *readonlySQLite) query(query string, header bool, emit func([]string) error) error {
	stmt, err := d.prepare(query)
	if err != nil {
		return err
	}
	defer sqlFinalize.Call(stmt)
	countRaw, _, _ := sqlColCount.Call(stmt)
	if countRaw > 16384 {
		return fmt.Errorf("SQLite 查询包含 %d 列，超过 Excel 单表上限", countRaw)
	}
	count := int(countRaw)
	names := make([]string, count)
	for c := 0; c < count; c++ {
		ptr, _, _ := sqlColName.Call(stmt, uintptr(c))
		if ptr == 0 {
			return errors.New("获取 SQLite 列名失败")
		}
		names[c], err = sqliteCString(ptr)
		if err != nil {
			return err
		}
	}
	if header {
		if err := emit(names); err != nil {
			return err
		}
	}
	for {
		code, _, _ := sqlStep.Call(stmt)
		if code == sqlDone {
			return nil
		}
		if code != sqlRow {
			return fmt.Errorf("SQLite 扫描失败（%d）：%s", code, sqliteLastError(d.ptr))
		}
		values := make([]string, count)
		for c := 0; c < count; c++ {
			t, _, _ := sqlColType.Call(stmt, uintptr(c))
			switch t {
			case sqlNull:
				values[c] = "[NULL]"
			case sqlBlob:
				length, _, _ := sqlColBytes.Call(stmt, uintptr(c))
				values[c] = fmt.Sprintf("[BLOB %d bytes]", length)
			default:
				ptr, _, _ := sqlColText.Call(stmt, uintptr(c))
				length, _, _ := sqlColBytes.Call(stmt, uintptr(c))
				if length == 0 {
					continue
				}
				data, e := sqliteReadLocal(ptr, int(length))
				if e != nil {
					return e
				}
				values[c] = string(data)
			}
		}
		if err := emit(values); err != nil {
			return err
		}
	}
}
func sqliteDiscover(path string) ([]sqliteTable, error) {
	db, err := openReadonlySQLite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var tables []sqliteTable
	err = db.query(`SELECT name,type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY CASE type WHEN 'table' THEN 0 ELSE 1 END,name COLLATE NOCASE`, false, func(record []string) error {
		if len(record) == 2 {
			tables = append(tables, sqliteTable{Name: record[0], Kind: record[1]})
		}
		if len(tables) > 500 {
			return errors.New("数据表数量超过 500，请分拆数据库")
		}
		return nil
	})
	return tables, err
}
func sqliteStreamRows(path, table string, emit func([]string) error) error {
	db, err := openReadonlySQLite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.query(`SELECT * FROM `+quoteSQLiteIdentifier(table), true, emit)
}

// GB18030 (Windows codepage 54936) covers GBK/CP936 exports as a subset.
// Conversion is performed by the OS without any downloadable dependency.
var winMultiByteToWide = syscall.NewLazyDLL("kernel32.dll").NewProc("MultiByteToWideChar")

func decodeGB18030(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if len(data) > 64<<20 {
		return "", errors.New("单行超过 64MB 解码上限")
	}
	decode := func(codePage uintptr) (string, error) {
		n, _, e := winMultiByteToWide.Call(codePage, 0, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), 0, 0)
		if n == 0 {
			return "", e
		}
		chars := make([]uint16, int(n))
		done, _, e := winMultiByteToWide.Call(codePage, 0, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&chars[0])), n)
		runtime.KeepAlive(data)
		if done == 0 {
			return "", e
		}
		return syscall.UTF16ToString(append(chars, 0)), nil
	}
	if s, e := decode(54936); e == nil {
		return s, nil
	}
	return decode(936)
}
