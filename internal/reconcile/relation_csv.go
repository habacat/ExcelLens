package reconcile

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"excellens/internal/formats"
)

// RelationCSVWriter writes one complete relation record per CSV row. Values
// remain ordinary columns; aligned JSON arrays retain original source columns,
// types, existence and uncertainty without multiplying the result rows.
type RelationCSVWriter struct {
	out     *csv.Writer
	query   RelationQuery
	sources [2]SourceInfo
	protect func(string) string
	row     []string
	columns [2]string
	states  relationCSVStateCache
	Rows    int64
}

func NewRelationCSVWriter(dst io.Writer, relation *Relation, protect func(string) string) (*RelationCSVWriter, error) {
	if dst == nil || relation == nil {
		return nil, errors.New("关联CSV缺少输出器或已完成预检")
	}
	w := &RelationCSVWriter{out: csv.NewWriter(dst), query: relation.Query(), sources: relation.Sources(), protect: protect}
	header := []string{"结果序号", "关联状态", "原因", "右侧匹配数"}
	for side, columns := range [][]int{w.query.LeftColumns, w.query.RightColumns} {
		encoded, _ := json.Marshal(columns)
		w.columns[side] = string(encoded)
		label := "左"
		if side == 1 {
			label = "右"
		}
		for _, name := range []string{"文件", "来源身份", "来源指纹", "快照", "工作表", "源行", "记录存在", "源列(JSON)", "读取类型(JSON)", "单元格存在(JSON)", "缺少缓存(JSON)", "类型未保留(JSON)", "原文截断(JSON)", "读取状态(JSON)"} {
			header = append(header, label+name)
		}
		for _, column := range columns {
			name := label + " " + fieldName(w.sources[side], column)
			if protect != nil {
				name += "（文本保护）"
			}
			header = append(header, name)
		}
	}
	if err := w.out.Write(header); err != nil {
		return nil, err
	}
	w.row = make([]string, len(header))
	return w, nil
}

func (w *RelationCSVWriter) Write(row RelationRow) error {
	if row.Sequence != w.Rows+1 || row.Sequence > formats.RelationOutputRows || row.Left == nil && row.Right == nil {
		return errors.New("关联CSV记录序号或来源无效")
	}
	status := row.Status
	switch row.Status {
	case "matched":
		status = "已匹配"
	case "unmatched":
		status = "未匹配"
	case "invalid_key":
		status = "无效关联键"
	default:
		return errors.New("关联CSV记录状态无效")
	}
	// csv.Writer consumes the record synchronously. Reuse only our string slots
	// and release every borrowed value on success or failure before returning.
	defer clear(w.row)
	out := append(w.row[:0], strconv.FormatInt(row.Sequence, 10), status, row.Reason, strconv.FormatInt(row.RightMatches, 10))
	for side, record := range []*Record{row.Left, row.Right} {
		info, columns := w.sources[side], w.query.LeftColumns
		if side == 1 {
			columns = w.query.RightColumns
		}
		out = append(out, info.Name, info.ID, info.Fingerprint, info.Stamp, info.Sheet)
		if record == nil {
			out = append(out, "", "false", "", "", "", "", "", "", "")
			for range columns {
				out = append(out, "")
			}
			continue
		}
		if record.Number < 1 || record.Number > formats.ExternalRows {
			return errors.New("关联CSV源行号无效")
		}
		state := w.states.encode(record, columns)
		out = append(out, strconv.Itoa(record.Number), "true", w.columns[side])
		out = append(out, state[:]...)
		for _, column := range columns {
			out = append(out, record.Values[column].Raw)
		}
	}
	if w.protect != nil {
		for i, value := range out {
			out[i] = w.protect(value)
		}
	}
	if err := w.out.Write(out); err != nil {
		return err
	}
	w.Rows++
	return nil
}

func (w *RelationCSVWriter) Flush() error { w.out.Flush(); return w.out.Error() }
