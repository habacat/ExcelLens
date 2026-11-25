package onlinedb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"excellens/internal/formats"
)

type TableRef struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
}

type Table struct {
	TableRef
	Kind string `json:"kind"`
}

type Catalogue struct {
	Tables    []Table `json:"tables"`
	Truncated bool    `json:"truncated,omitempty"`
}

type Column struct {
	Name, Type       string
	Nullable         *bool
	Precision, Scale *int64
}

type ReadDefinition struct {
	Query   string
	Columns []Column
}

func (c *Connection) Tables(ctx context.Context) (Catalogue, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	query, args := c.catalogueQuery()
	result := Catalogue{Tables: make([]Table, 0)}
	rows, err := c.db.QueryContext(c.readContext(ctx), query, args...)
	if err != nil {
		return result, databaseError(err, "无法读取数据表目录，请核对数据库和架构权限")
	}
	defer rows.Close()
	seen := make(map[TableRef]bool)
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Schema, &t.Name, &t.Kind); err != nil {
			return result, databaseError(err, "数据库返回的目录类型无法读取")
		}
		if !validIdentifier(t.Schema) || !validIdentifier(t.Name) {
			return result, errors.New("数据库返回了无法读取的表或架构名称")
		}
		if seen[t.TableRef] {
			continue
		}
		if len(result.Tables) == formats.DatabaseCatalogueEntries {
			result.Truncated = true
			break
		}
		seen[t.TableRef] = true
		if strings.Contains(strings.ToUpper(t.Kind), "VIEW") {
			t.Kind = "view"
		} else {
			t.Kind = "table"
		}
		result.Tables = append(result.Tables, t)
	}
	if err := rows.Err(); err != nil {
		return result, databaseError(err, "数据表目录读取中断")
	}
	return result, nil
}

func (c *Connection) catalogueQuery() (string, []any) {
	var query, schema, param string
	switch c.engine.ID {
	case "mysql":
		query = `SELECT TABLE_SCHEMA,TABLE_NAME,TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_TYPE IN ('BASE TABLE','VIEW','SYSTEM VERSIONED') AND TABLE_SCHEMA NOT IN ('information_schema','performance_schema','mysql','sys')`
		if c.withDatabase {
			query += " AND TABLE_SCHEMA=DATABASE()"
		}
		schema, param = "TABLE_SCHEMA", "?"
	case "postgres":
		query = `SELECT n.nspname::text,c.relname::text,CASE WHEN c.relkind IN ('v','m') THEN 'view' ELSE 'table' END FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE c.relkind IN ('r','p','v','m','f') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND n.nspname NOT LIKE 'pg_temp%' AND has_schema_privilege(n.oid,'USAGE') AND has_table_privilege(c.oid,'SELECT')`
		schema, param = "n.nspname", "$1"
	case "oracle":
		query = `SELECT OWNER,OBJECT_NAME,MAX(OBJECT_TYPE) FROM ALL_OBJECTS WHERE OBJECT_TYPE IN ('TABLE','VIEW','MATERIALIZED VIEW') AND OWNER NOT IN ('SYS','SYSTEM','XDB','MDSYS','CTXSYS','OUTLN')`
		schema, param = "OWNER", ":1"
	case "sqlserver":
		query = fmt.Sprintf(`SELECT TOP (%d) TABLE_SCHEMA,TABLE_NAME,TABLE_TYPE FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_TYPE IN ('BASE TABLE','VIEW')`, formats.DatabaseCatalogueEntries+1)
		schema, param = "TABLE_SCHEMA", "@p1"
	case "clickhouse":
		query = `SELECT database,name,engine FROM system.tables WHERE database NOT IN ('system','INFORMATION_SCHEMA','information_schema') AND is_temporary=0`
		if c.withDatabase {
			query += " AND database=currentDatabase()"
		}
		schema, param = "database", "?"
	}
	var args []any
	if c.schema != "" {
		query += " AND " + schema + "=" + param
		args = []any{c.schema}
	}
	if c.engine.ID == "oracle" {
		query += " GROUP BY OWNER,OBJECT_NAME"
	}
	query += " ORDER BY 1,2"
	switch c.engine.ID {
	case "mysql", "postgres", "clickhouse":
		query += fmt.Sprintf(" LIMIT %d", formats.DatabaseCatalogueEntries+1)
	case "oracle":
		// ROWNUM keeps catalogue discovery working on servers before 12c.
		query = fmt.Sprintf("SELECT * FROM (%s) WHERE ROWNUM<=%d", query, formats.DatabaseCatalogueEntries+1)
	}
	return query, args
}

func validIdentifier(text string) bool {
	return text != "" && len(text) <= 512 && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

func (c *Connection) quote(name string) string {
	switch c.engine.ID {
	case "mysql":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case "clickhouse":
		// ClickHouse quoted identifiers also interpret backslash escapes.
		return "`" + strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(name) + "`"
	case "sqlserver":
		return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// ReadTable streams the column names first, then one row at a time. Only
// identifiers discovered in the current catalogue are passed by the caller.
func (c *Connection) ReadTable(ctx context.Context, table TableRef, emit func([]Value) error) error {
	return c.StreamTable(ctx, table, func(def ReadDefinition) error {
		header := make([]Value, len(def.Columns))
		for i, col := range def.Columns {
			header[i] = Value{Kind: Text, Raw: col.Name}
		}
		return emit(header)
	}, emit)
}

func (c *Connection) StreamTable(ctx context.Context, table TableRef, begin func(ReadDefinition) error, emit func([]Value) error) error {
	if !validIdentifier(table.Schema) || !validIdentifier(table.Name) {
		return errors.New("表或架构名称无效")
	}
	query := "SELECT * FROM " + c.quote(table.Schema) + "." + c.quote(table.Name)
	rows, err := c.db.QueryContext(c.readContext(ctx), query)
	if err != nil {
		return databaseError(err, "无法读取该表，请核对数据表权限或重新连接")
	}
	defer rows.Close()
	columns, err := rows.ColumnTypes()
	if err != nil || len(columns) == 0 || len(columns) > formats.ExcelColumns {
		return errors.New("数据表的列定义无法读取或超出列数容量")
	}
	values := make([]Value, len(columns))
	def := ReadDefinition{Query: query, Columns: make([]Column, len(columns))}
	kinds := make([]columnKind, len(columns))
	raw := make([]any, len(columns))
	targets := make([]any, len(columns))
	for i, column := range columns {
		targets[i] = &raw[i]
		item := Column{Name: column.Name(), Type: column.DatabaseTypeName()}
		if v, ok := column.Nullable(); ok {
			item.Nullable = &v
		}
		if p, s, ok := column.DecimalSize(); ok {
			item.Precision, item.Scale = &p, &s
		}
		def.Columns[i] = item
		kinds[i] = databaseColumnKind(c.engine.ID, item.Type)
	}
	if err := begin(def); err != nil {
		return err
	}
	row := 1
	for rows.Next() {
		row++
		if err := rows.Scan(targets...); err != nil {
			return databaseError(err, fmt.Sprintf("第 %d 行的数据类型无法读取", row))
		}
		for i, value := range raw {
			values[i], err = readValue(kinds[i], value)
			if err != nil {
				return fmt.Errorf("第 %d 行第 %d 列：%w", row, i+1, err)
			}
		}
		clear(raw)
		if err := emit(values); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return databaseError(err, "读取数据表时连接中断")
	}
	return nil
}
