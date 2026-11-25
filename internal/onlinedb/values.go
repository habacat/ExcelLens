package onlinedb

import (
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"excellens/internal/formats"

	mssql "github.com/microsoft/go-mssqldb"
)

type Value struct {
	Kind byte
	Raw  string
}

const (
	Text    = 't'
	Number  = 'n'
	Boolean = 'b'
	Null    = '0'
	Binary  = 'x'
	Date    = 'd'
)

type columnKind uint8

const (
	columnOther columnKind = iota
	columnDecimal
	columnBinary
	columnGUID
	columnDate
	columnTime
	columnTimestamp
	columnTimestampZone
)

func databaseColumnKind(engine, name string) columnKind {
	for strings.HasPrefix(name, "Nullable(") || strings.HasPrefix(name, "LowCardinality(") {
		_, name, _ = strings.Cut(name, "(")
		name = strings.TrimSuffix(name, ")")
	}
	name = strings.ToUpper(strings.TrimSpace(name))
	if strings.HasPrefix(name, "DATETIME64(") && strings.Contains(name, "'") || strings.HasPrefix(name, "DATETIME(") {
		return columnTimestampZone
	}
	name, _, _ = strings.Cut(name, "(")
	switch name {
	case "DECIMAL", "DEC", "DECIMAL32", "DECIMAL64", "DECIMAL128", "DECIMAL256", "NUMERIC", "NUMBER", "MONEY", "SMALLMONEY", "UNSIGNED BIGINT",
		"INT128", "UINT128", "INT256", "UINT256":
		return columnDecimal
	case "BYTEA", "BIT", "VARBIT", "RAW", "LONG RAW", "LONGRAW", "LONGVARRAW", "BINARY", "VARBINARY", "IMAGE", "BLOB", "OCIBLOBLOCATOR", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB":
		if engine != "sqlserver" || name != "BIT" {
			return columnBinary
		}
	case "UNIQUEIDENTIFIER":
		if engine == "sqlserver" {
			return columnGUID
		}
	case "DATE", "DATE32":
		if engine == "oracle" {
			return columnTimestamp // Oracle DATE also has hour/minute/second.
		}
		return columnDate
	case "TIME", "TIMETZ", "TIME WITH TIME ZONE", "TIME64":
		return columnTime
	case "TIMESTAMP", "DATETIME", "DATETIME2", "SMALLDATETIME", "TIMESTAMPDTY", "DATETIME64":
		if engine != "sqlserver" || name != "TIMESTAMP" {
			return columnTimestamp
		}
		return columnBinary // SQL Server TIMESTAMP is a row version.
	case "TIMESTAMPTZ", "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITH LOCAL TIME ZONE", "DATETIMEOFFSET", "TIMESTAMPTZ_DTY", "TIMESTAMPLTZ_DTY", "TIMESTAMPELTZ":
		return columnTimestampZone
	}
	return columnOther
}

var decimalLiteral = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([Ee][+-]?[0-9]+)?$`)

func readValue(kind columnKind, raw any) (Value, error) {
	if raw == nil {
		return Value{Kind: Null}, nil
	}
	var out Value
	switch v := raw.(type) {
	case time.Time:
		out.Kind = Date
		switch kind {
		case columnDate:
			out.Raw = v.Format(time.DateOnly)
		case columnTime:
			out.Kind, out.Raw = Text, v.Format("15:04:05.999999999")
		case columnTimestamp:
			out.Raw = v.Format("2006-01-02T15:04:05.999999999")
		default:
			out.Raw = v.Format(time.RFC3339Nano)
		}
	case bool:
		out = Value{Kind: Boolean, Raw: "0"}
		if v {
			out.Raw = "1"
		}
	case int64:
		out = Value{Kind: Number, Raw: strconv.FormatInt(v, 10)}
	case uint64:
		out = Value{Kind: Number, Raw: strconv.FormatUint(v, 10)}
	case float64:
		return databaseFloat(kind, v, 64)
	case float32:
		return databaseFloat(kind, float64(v), 32)
	case []byte:
		if kind == columnGUID {
			var id mssql.UniqueIdentifier
			if err := id.Scan(v); err != nil {
				return Value{}, errors.New("SQL Server 标识符的字节长度无效")
			}
			return Value{Kind: Text, Raw: id.String()}, nil
		}
		if kind == columnBinary {
			if len(v) > (formats.DatabaseCellBytes-2)/2 {
				return Value{}, errors.New("二进制值超过读取容量")
			}
			return Value{Kind: Binary, Raw: "0x" + hex.EncodeToString(v)}, nil
		}
		out = Value{Kind: Text, Raw: string(v)}
	case string:
		out = Value{Kind: Text, Raw: v}
	case driver.Valuer:
		value, err := v.Value()
		if err != nil || !driver.IsValue(value) {
			return Value{}, errors.New("数据库驱动的原始值无法表示")
		}
		return readValue(kind, value)
	case fmt.Stringer:
		out = Value{Kind: Text, Raw: v.String()}
	default:
		return readExtendedValue(kind, v)
	}
	if len(out.Raw) > formats.DatabaseCellBytes {
		return Value{}, errors.New("单元格超过读取容量")
	}
	if !utf8.ValidString(out.Raw) {
		return Value{}, errors.New("数据库返回的文本编码无效")
	}
	if out.Kind == Text && kind == columnDecimal {
		if decimalLiteral.MatchString(out.Raw) {
			out.Kind = Number
		} else if out.Raw != "NaN" && out.Raw != "Infinity" && out.Raw != "-Infinity" {
			return Value{}, errors.New("精确数字的表示无效")
		}
	}
	if out.Kind == Text && (kind == columnDate || kind == columnTimestamp || kind == columnTimestampZone) {
		// Some drivers expose the wire representation as raw text. Keep zero
		// dates, infinity and other non-calendar markers as their source text.
		if _, err := time.Parse(time.DateOnly, out.Raw); kind == columnDate && err == nil {
			out.Kind = Date
		} else if kind == columnTimestamp {
			candidate := strings.Replace(out.Raw, " ", "T", 1)
			if _, err := time.Parse("2006-01-02T15:04:05.999999999", candidate); err == nil {
				out.Kind, out.Raw = Date, candidate
			}
		} else if kind == columnTimestampZone {
			if _, err := time.Parse(time.RFC3339Nano, out.Raw); err == nil {
				out.Kind = Date
			}
		}
	}
	return out, nil
}

func databaseFloat(kind columnKind, v float64, bits int) (Value, error) {
	if kind == columnDecimal {
		return Value{}, errors.New("数据库驱动未提供精确数字的原值")
	}
	value := Value{Kind: Number, Raw: strconv.FormatFloat(v, 'g', -1, bits)}
	if math.IsInf(v, 0) || math.IsNaN(v) {
		value.Kind = Text
	}
	return value, nil
}

func readExtendedValue(kind columnKind, value any) (Value, error) {
	// ClickHouse also returns widths other than the standard driver.Value
	// integer types, as well as arrays and maps from its native protocol.
	r := reflect.ValueOf(value)
	switch r.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return Value{Kind: Number, Raw: strconv.FormatInt(r.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return Value{Kind: Number, Raw: strconv.FormatUint(r.Uint(), 10)}, nil
	case reflect.Pointer:
		if r.IsNil() {
			return Value{Kind: Null}, nil
		}
		return readValue(kind, r.Elem().Interface())
	case reflect.Slice, reflect.Array, reflect.Map:
		data, err := json.Marshal(value)
		if err == nil && len(data) <= formats.DatabaseCellBytes {
			return Value{Kind: Text, Raw: string(data)}, nil
		}
	}
	return Value{}, errors.New("数据库值的类型无法读取或超过容量")
}
