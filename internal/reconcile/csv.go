package reconcile

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"excellens/internal/workbook"
)

// CSVWriter serializes complete, borrowed comparison records. UI samples never
// participate in this path. Protection, when supplied, is explicitly labelled.
type CSVWriter struct {
	out         *csv.Writer
	query       Query
	left, right SourceInfo
	protect     func(string) string
	pairs       []Pair
	pairNames   []string
	Rows        int64
}

func NewCSVWriter(dst io.Writer, query Query, left, right SourceInfo, protect func(string) string) (*CSVWriter, error) {
	writer := &CSVWriter{out: csv.NewWriter(dst), query: query, left: left, right: right, protect: protect}
	if len(query.Keys) == 0 {
		return nil, errors.New("CSV核对主键为空")
	}
	writer.pairs = query.Fields
	if len(writer.pairs) == 0 {
		writer.pairs = query.Keys[:1]
	}
	for _, pair := range writer.pairs {
		writer.pairNames = append(writer.pairNames, fieldName(left, pair.Left)+" / "+fieldName(right, pair.Right))
	}
	header := []string{"记录类别", "组结果", "字段结果", "主键组ID", "主键原文(JSON)", "左文件", "左来源指纹", "左快照", "左工作表", "左源行", "左源格", "左读取类型", "左单元格存在", "左读取值", "右文件", "右来源指纹", "右快照", "右工作表", "右源行", "右源格", "右读取类型", "右单元格存在", "右读取值", "左记录数", "右记录数", "比较字段", "精确差额(左减右)", "原因"}
	if protect != nil {
		header[13] += "（文本保护）"
		header[22] += "（文本保护）"
	}
	if err := writer.out.Write(header); err != nil {
		return nil, err
	}
	return writer, nil
}

func StatusLabel(status string) string {
	switch status {
	case "equal":
		return "一致"
	case "different":
		return "差异"
	case "left_only":
		return "仅左侧"
	case "right_only":
		return "仅右侧"
	case "duplicate":
		return "重复主键"
	case "invalid_key":
		return "无效主键"
	case "review":
		return "需核对"
	}
	return status
}

func fieldName(info SourceInfo, column int) string {
	for _, field := range info.Fields {
		if field.Column == column {
			return workbook.ColumnName(column) + " " + field.Name
		}
	}
	return workbook.ColumnName(column)
}

func (writer *CSVWriter) row(kind, status, fieldStatus, keyID, keyJSON string, left, right *Record, leftCol, rightCol int, leftCount, rightCount, field, delta, reason string) error {
	row := []string{kind, StatusLabel(status), fieldStatus, keyID, keyJSON}
	appendSource := func(info SourceInfo, record *Record, column int) {
		row = append(row, info.Name, info.Fingerprint, info.Stamp, info.Sheet)
		if record == nil || column < 1 {
			row = append(row, "", "", "", "", "")
			return
		}
		value := record.Values[column]
		row = append(row, strconv.Itoa(record.Number), workbook.ColumnName(column)+strconv.Itoa(record.Number), value.Type, strconv.FormatBool(value.Exists), value.Raw)
		var state string
		if value.MissingCache {
			state = "公式缺少缓存值"
		}
		if value.Uncertain != "" {
			state = value.Uncertain
		}
		if value.TypeMissing && state == "" {
			state = "源类型未保留，按所选核对方式读取"
		}
		if state != "" && !strings.Contains(reason, state) {
			if reason != "" {
				reason += "；"
			}
			reason += state
		}
	}
	appendSource(writer.left, left, leftCol)
	appendSource(writer.right, right, rightCol)
	row = append(row, leftCount, rightCount, field, delta, reason)
	if writer.protect != nil {
		for i, text := range row {
			row[i] = writer.protect(text)
		}
	}
	if err := writer.out.Write(row); err != nil {
		return err
	}
	writer.Rows++
	return nil
}

func (writer *CSVWriter) Sinks() Sinks {
	return Sinks{Group: func(group Group) error {
		encoded, _ := json.Marshal(group.Key)
		keyJSON := string(encoded)
		leftCount, rightCount := strconv.FormatInt(group.LeftCount, 10), strconv.FormatInt(group.RightCount, 10)
		if group.Status == "duplicate" {
			return writer.row("主键汇总", group.Status, "未配对", group.KeyID, keyJSON, nil, nil, 0, 0, leftCount, rightCount, "", "", group.Reason)
		}
		for i, pair := range writer.pairs {
			status, delta, reason := StatusLabel(group.Status), "", group.Reason
			if len(writer.query.Fields) > 0 && i < len(group.Comparisons) {
				comparison := group.Comparisons[i]
				delta, reason = comparison.Delta, comparison.Reason
				status = "差异"
				if comparison.Equal {
					status = "一致"
				}
				if comparison.Reason != "" {
					status = "需核对"
				}
			}
			if err := writer.row("核对记录", group.Status, status, group.KeyID, keyJSON, group.Left, group.Right, pair.Left, pair.Right, leftCount, rightCount, writer.pairNames[i], delta, reason); err != nil {
				return err
			}
		}
		return nil
	}, Duplicate: func(keyID, side string, record Record) error {
		var left, right *Record
		isRight := side == "right"
		if isRight {
			right = &record
		} else {
			left = &record
		}
		var key []string
		for _, pair := range writer.query.Keys {
			col := pair.Left
			if isRight {
				col = pair.Right
			}
			key = append(key, record.Values[col].Raw)
		}
		encoded, _ := json.Marshal(key)
		keyJSON := string(encoded)
		for i, pair := range writer.pairs {
			if err := writer.row("重复键记录", "duplicate", "未配对", keyID, keyJSON, left, right, pair.Left, pair.Right, "", "", writer.pairNames[i], "", "逐条保留来源，未生成多对多组合"); err != nil {
				return err
			}
		}
		return nil
	}}
}

func (writer *CSVWriter) Flush() error { writer.out.Flush(); return writer.out.Error() }
