package analysis

import (
	"encoding/json"
	"strconv"
)

// SourceOrigin is immutable and shared by records from one append input. Row
// numbers remain physical source rows; Sequence is kept separately on records.
// Columns maps result fields to physical source columns; zero means no binding.
type SourceOrigin struct {
	Input       int    `json:"input"`
	Book        string `json:"book"`
	Fingerprint string `json:"fingerprint"`
	File        string `json:"file"`
	Source      string `json:"source"`
	Sheet       string `json:"sheet"`
	Stamp       string `json:"stamp"`
	Columns     []int  `json:"columns"`
}

func detailOriginColumns(q Query) []string {
	if q.Composition == nil {
		return []string{"源行号"}
	}
	return []string{"联合序号", "来源文件", "来源表", "源工作簿", "源行号", "源列映射"}
}

func detailOriginCells(rec Record, fields []Field) []string {
	if rec.Origin == nil {
		return []string{strconv.Itoa(rec.Number)}
	}
	columns := make([]int, len(fields))
	for i, field := range fields {
		if field.Column >= 1 && field.Column <= len(rec.Origin.Columns) {
			columns[i] = rec.Origin.Columns[field.Column-1]
		}
	}
	mapping, _ := json.Marshal(columns)
	return []string{strconv.FormatInt(rec.Sequence, 10), rec.Origin.File, rec.Origin.Sheet, rec.Origin.Book, strconv.Itoa(rec.Number), string(mapping)}
}

func IssueColumnsForQuery(q Query) []string {
	if q.Composition == nil {
		return IssueColumns()
	}
	columns := append([]string{"来源文件", "源指纹", "来源表", "源版本"}, IssueColumns()...)
	return append(columns, "联合序号")
}

func IssueCellsForQuery(q Query, issue Issue) []string {
	if q.Composition == nil {
		return IssueCells(issue)
	}
	origin := issue.Origin
	if origin == nil {
		origin = &SourceOrigin{}
	}
	row := append([]string{origin.File, origin.Fingerprint, origin.Sheet, origin.Stamp}, IssueCells(issue)...)
	return append(row, strconv.FormatInt(issue.Sequence, 10))
}
