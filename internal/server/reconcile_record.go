package server

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/formats"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

type reconcileRecord struct {
	Kind                  string                `json:"kind"`
	Version               int                   `json:"version"`
	RuleVersion           int                   `json:"ruleVersion"`
	Application           string                `json:"applicationVersion"`
	Started               time.Time             `json:"started"`
	Inputs                [2]reconcileInput     `json:"inputs"`
	Query                 reconcile.Query       `json:"query"`
	Scope                 string                `json:"scope"`
	Counts                reconcile.Counts      `json:"counts"`
	ExceptionGroupsSHA256 string                `json:"exceptionGroupsSHA256"`
	Detail                reconcileRecordDetail `json:"detail"`
}

type reconcileInput struct {
	reconcile.SourceInfo
	FileSHA256 string `json:"fileSHA256,omitempty"`
}

type reconcileRecordDetail struct {
	File       string `json:"file"`
	SHA256     string `json:"sha256"`
	Status     string `json:"status"`
	ProtectCSV bool   `json:"protectCSV"`
	Rows       int64  `json:"rows"`
}

func captureReconcileRecord(job *reconcileTask, sources [2]reconcile.Source) (*reconcileRecord, error) {
	record := &reconcileRecord{Kind: "ExcelLens.Reconcile", Version: 1, RuleVersion: job.Query.Rules(), Application: buildinfo.Version, Started: job.Created.UTC(), Query: job.Query, Scope: "all-data-rows"}
	record.Query.RuleVersion = 0 // The manifest carries the actual rule version.
	digests := make(map[*workbook.Workbook]string)
	for i, source := range sources {
		markReconcileHeaders(source.Info)
		input := reconcileInput{SourceInfo: source.Info}
		input.Fields = nil
		for _, col := range reconcile.NeededColumns(job.Query, i == 1) {
			if col < 1 || col > len(source.Info.Fields) {
				return nil, fmt.Errorf("核对字段 %d 超出范围", col)
			}
			input.Fields = append(input.Fields, source.Info.Fields[col-1])
		}
		if input.Kind == "workbook" {
			book := job.Books[i]
			if digests[book] == "" {
				digest, err := book.SourceFileSHA256(job.ctx)
				if err != nil {
					return nil, fmt.Errorf("核验 %s 的文件摘要失败：%w", input.Name, err)
				}
				digests[book] = digest
			}
			input.FileSHA256 = digests[book]
		}
		record.Inputs[i] = input
	}
	return record, nil
}

func (record reconcileRecord) writeBundle(out io.Writer, status string, protect bool, writeCSV func(io.Writer) (int64, error)) error {
	archive := zip.NewWriter(&auditSizeWriter{out, formats.BatchArchiveBytes, "核查包超过 256 MB，可按类型导出或选择完整 CSV"})
	const name = "核对明细.csv"
	detail, err := archive.Create(name)
	if err != nil {
		return err
	}
	hash := sha256.New()
	detailOut := &auditSizeWriter{detail, formats.BatchDetailBytes, "核查明细超过 512 MB，可按类型导出或选择完整 CSV"}
	rows, err := writeCSV(io.MultiWriter(detailOut, hash))
	if err != nil {
		return err
	}
	record.Detail = reconcileRecordDetail{File: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Rows: rows, Status: status, ProtectCSV: protect}
	manifest, err := archive.Create("核查记录.json")
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(&auditSizeWriter{manifest, formats.ReconcileRecordBytes, "核查记录超过 1 MB，请减少所选字段或缩短表头"})
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		return err
	}
	return archive.Close()
}
