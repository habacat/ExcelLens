package server

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"

	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/i18n"
)

// Reconciliation and relation writers put both immutable input fingerprints in
// every output row. One record suffices even for a multi-gigabyte result.
func csvSnapshotReference(in io.Reader, key string) (bool, error) {
	reader := csv.NewReader(io.LimitReader(in, formats.ReconcileRecordBytes+1))
	header, err := reader.Read()
	if err != nil {
		return false, i18n.Wrap("service.databaseSnapshotExports.unableReadResultColumns", err)
	}
	left, right := slices.Index(header, "左来源指纹"), slices.Index(header, "右来源指纹")
	if left < 0 || right < 0 {
		return false, i18n.Error("service.databaseSnapshotExports.reconciliationOrCorrelationResultsLack")
	}
	record, err := reader.Read()
	if reader.InputOffset() > formats.ReconcileRecordBytes {
		return false, i18n.Error("service.databaseSnapshotExports.firstSourceRecordExportResult")
	}
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return record[left] == key || record[right] == key, nil
}

func exportedSnapshotReference(path, key string) (bool, error) {
	name := filepath.Base(path)
	relation := strings.Contains(name, "_关联结果_")
	reconcileFile := strings.Contains(name, "_双表核对_")
	if strings.HasSuffix(name, ".csv") && (relation || reconcileFile) {
		f, err := os.Open(path)
		if err != nil {
			return false, err
		}
		defer f.Close()
		return csvSnapshotReference(f, key)
	}
	if !strings.HasSuffix(name, ".zip") {
		return false, nil
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return false, err
	}
	defer archive.Close()
	var found, hasReconcileRecord, hasRelationRows bool
	for _, part := range archive.File {
		switch {
		case part.Name == "核查记录.json" || part.Name == "_ExcelLens_manifest.json":
			if part.UncompressedSize64 > formats.ReconcileRecordBytes {
				return false, i18n.Error("service.databaseSnapshotExports.exceededLimitExportedSourceList")
			}
			f, err := part.Open()
			if err != nil {
				return false, err
			}
			data, err := io.ReadAll(io.LimitReader(f, formats.ReconcileRecordBytes+1))
			f.Close()
			if err != nil || len(data) > formats.ReconcileRecordBytes {
				return false, i18n.Error("service.databaseSnapshotExports.unableReadExportedList")
			}
			if part.Name == "核查记录.json" {
				hasReconcileRecord = true
				var record reconcileRecord
				if err := json.Unmarshal(data, &record); err != nil {
					return false, err
				}
				if record.Kind != "ExcelLens.Reconcile" || record.Version != 1 {
					return false, i18n.Error("service.databaseSnapshotExports.unableRecognizeVersionVerificationRecord")
				}
				for _, input := range record.Inputs {
					if input.Fingerprint == "" {
						return false, i18n.Error("service.databaseSnapshotExports.verifyingRecordsMissingSourceFingerprints")
					}
					if input.Fingerprint == key || input.FileSHA256 == key {
						found = true
					}
				}
			} else {
				var record struct {
					Fingerprint string `json:"fingerprint"`
				}
				if err := json.Unmarshal(data, &record); err != nil {
					return false, err
				}
				if record.Fingerprint == "" {
					return false, i18n.Error("service.databaseSnapshotExports.exportListMissingSourceFingerprint")
				}
				found = found || record.Fingerprint == key
			}
		case relation && strings.HasSuffix(part.Name, ".csv"):
			f, err := part.Open()
			if err != nil {
				return false, err
			}
			hasRelationRows = true
			used, err := csvSnapshotReference(f, key)
			f.Close()
			if err != nil {
				return false, err
			}
			found = found || used
		}
	}
	if (reconcileFile && !hasReconcileRecord) || (relation && !hasRelationRows) {
		return false, i18n.Error("service.databaseSnapshotExports.exportPackageLacksSourceRecords")
	}
	return found, nil
}

func snapshotExportReferences(ctx context.Context, key string) ([]string, error) {
	entries, err := os.ReadDir(ExportDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.Type().IsRegular() {
			continue
		}
		found, err := exportedSnapshotReference(filepath.Join(ExportDir(), e.Name()), key)
		if err != nil {
			return nil, i18n.Wrap("service.databaseSnapshotExports.exportFile", err, i18n.Args{"name": e.Name()})
		}
		if found {
			refs = append(refs, "导出文件「"+e.Name()+"」")
		}
	}
	return refs, nil
}
