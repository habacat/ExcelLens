package workbook

// Header recognition affects presentation only. It never drops a source row,
// renames a field, changes its type or participates in the cache fingerprint.
import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type TextTableHeader struct {
	Row      int      `json:"row"`
	Columns  []string `json:"columns"`
	Detected bool     `json:"detected"`
}

func headerLabel(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > 256 {
		value = string([]rune(value)[:256]) + "…"
	}
	return value
}

func textHeader(row int, fields []string, detected bool) *TextTableHeader {
	labels := make([]string, min(len(fields), 16384))
	for i := range labels {
		labels[i] = headerLabel(fields[i])
	}
	return &TextTableHeader{Row: row, Columns: labels, Detected: detected}
}

func (w *Workbook) detectTextTableHeader(sample string) *TextTableHeader {
	if w.source.fixed != nil {
		return textHeader(1, w.source.fixed.header, true)
	}
	if w.source.delimiter == 0 {
		return nil
	}
	rows := make([][]string, 0, 12)
	done := errors.New("header sample complete")
	_ = w.streamTextRecords(strings.NewReader(sample), func(fields []string) error {
		rows = append(rows, append([]string(nil), fields...))
		if len(rows) >= 12 {
			return done
		}
		return nil
	})
	if len(rows) == 0 {
		return nil
	}
	if w.source.oracle != nil {
		return textHeader(1, rows[0], true)
	}
	for i := 0; i < len(rows)-1 && i < 5; i++ {
		header := rows[i]
		if len(header) < 2 || len(header) > 16384 {
			continue
		}
		// Before a candidate, accept only blank/single-cell report titles.
		prefixOK := true
		for _, prefix := range rows[:i] {
			nonempty := 0
			for _, value := range prefix {
				if strings.TrimSpace(value) != "" {
					nonempty++
				}
			}
			if nonempty > 1 {
				prefixOK = false
				break
			}
		}
		if !prefixOK {
			continue
		}
		named, known, contrast := 0, 0, 0
		unique := make(map[string]bool, len(header))
		valid := true
		for col, value := range header {
			name := strings.TrimSpace(value)
			if name == "" {
				continue
			}
			if !headerLikeLabel(name) || unique[strings.ToLower(name)] {
				valid = false
				break
			}
			unique[strings.ToLower(name)] = true
			named++
			if knownHeaderLabel(name) {
				known++
			}
			typed, nonblank := 0, 0
			for _, data := range rows[i+1:] {
				if len(data) != len(header) {
					continue
				}
				value := strings.TrimSpace(data[col])
				if value == "" {
					continue
				}
				nonblank++
				if headerTypedValue(value) {
					typed++
				}
			}
			if typed >= 2 && typed*2 >= nonblank {
				contrast++
			}
		}
		if !valid || named*5 < len(header)*4 {
			continue
		}
		following := 0
		shapeOK := true
		for _, data := range rows[i+1:] {
			if len(data) != len(header) {
				shapeOK = false
				break
			}
			following++
		}
		// A partial final CSV record in the bounded sample is omitted by the
		// shared parser. Complete inconsistent rows are not evidence of a table.
		if !shapeOK {
			continue
		}
		strongNames := known >= 2 && known*2 >= named
		if strongNames && following >= 1 || following >= 2 && contrast >= max(1, (named+2)/3) {
			return textHeader(i+1, header, true)
		}
	}
	return nil
}

func headerLikeLabel(value string) bool {
	if utf8.RuneCountInString(value) > 80 || strings.ContainsAny(value, "\r\n\t") || headerTypedValue(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func headerTypedValue(value string) bool {
	if _, ok := parseFilterNumber(value); ok {
		return true
	}
	switch strings.ToLower(value) {
	case "true", "false", "null":
		return true
	}
	for _, layout := range []string{"2006-01-02", "2006/01/02", "02-01-06", "2006-01-02 15:04:05", "2006/01/02 15:04:05", "15:04:05"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func knownHeaderLabel(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "id", "name", "date", "time", "amount", "value", "code", "description", "account", "account_name", "account_code", "currency", "debit", "credit", "balance", "status", "type", "number", "quantity", "price", "total", "department", "city", "email", "姓名", "城市", "地址", "性别", "备注", "摘要", "单号", "账号", "帐号", "单位", "数量", "单价", "状态", "借方", "贷方", "借", "贷":
		return true
	}
	for _, word := range []string{"金额", "余额", "名称", "编号", "日期", "币种", "科目", "部门", "机构", "凭证", "创建人", "审批人", "过帐人", "过账人", "时间"} {
		if strings.Contains(value, word) {
			return true
		}
	}
	return false
}

// Explicit user-selected headers may be ambiguous; unlike auto detection this
// merely reads their labels from the indexed row. Source cells stay untouched.
func (w *Workbook) ReadTextTableHeader(ctx context.Context, meta SheetMeta, number int) (*TextTableHeader, error) {
	if w.Format != "txt" {
		return nil, errors.New("此入口用于 TXT 表头")
	}
	idx := w.GetCachedIndex(meta.Path)
	if idx == nil {
		return nil, errors.New("请等待完整索引建立完成")
	}
	pos := idx.FindRowOffset(number)
	if number < 1 || number > idx.MaxRow || pos >= idx.RowCount() || idx.RowAt(pos).Number != number {
		return nil, errors.New("表头行超出数据范围")
	}
	rows, err := w.ReadRowsContext(ctx, idx, pos, 1, 1, min(16384, max(1, idx.MaxCol)))
	if err != nil {
		return nil, err
	}
	fields := make([]string, min(16384, max(1, idx.MaxCol)))
	if len(rows) > 0 {
		for _, cell := range rows[0].Cells {
			if cell.Col >= 1 && cell.Col <= len(fields) {
				fields[cell.Col-1] = cell.Display
			}
		}
	}
	return textHeader(number, fields, false), nil
}
