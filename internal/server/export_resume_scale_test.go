package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func millionExportExpected(source int) []string {
	if source == 0 {
		return []string{"编号", "金额", "分组"}
	}
	group := "keep"
	if source%7 == 0 {
		group = "drop"
	}
	return []string{fmt.Sprintf("000%09d", source), fmt.Sprintf("9007199254740993.%02d", source%17), group}
}

func verifyMillionResumedCSV(t *testing.T, data []byte, count int) {
	t.Helper()
	reader := csv.NewReader(bufio.NewReaderSize(bytes.NewReader(data), 1<<20))
	for source := count; source >= 0; source-- {
		row, err := reader.Read()
		want := millionExportExpected(source)
		if err != nil || len(row) != 3 || row[0] != want[0] || row[1] != want[1] || row[2] != want[2] {
			t.Fatal("million CSV output differs from independent exact string/integer oracle", source, row, want, err)
		}
	}
	if _, err := reader.Read(); err != io.EOF {
		t.Fatal("million CSV has an incomplete or extra tail", err)
	}
}

func verifyMillionResumedXLSX(t *testing.T, data []byte, count int) uint64 {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var sheet *zip.File
	for _, entry := range archive.File {
		if entry.Name == "xl/worksheets/sheet1.xml" {
			sheet = entry
		}
	}
	if sheet == nil || sheet.UncompressedSize64 <= uint64(formats.CheckpointBlockBytes) {
		t.Fatal("scale fixture did not exercise a worksheet part larger than one physical checkpoint block")
	}
	source, err := sheet.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	decoder := xml.NewDecoder(bufio.NewReaderSize(source, 1<<20))
	row, column := 0, 0
	var want []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("XLSX worksheet XML/CRC is incomplete", err)
		}
		switch node := token.(type) {
		case xml.StartElement:
			switch node.Name.Local {
			case "row":
				row++
				column = 0
				for _, attribute := range node.Attr {
					if attribute.Name.Local == "r" && attribute.Value != strconv.Itoa(row) {
						t.Fatal("compacted XLSX output coordinate reset after recovery", row, attribute.Value)
					}
				}
				want = millionExportExpected(count + 1 - row)
			case "c":
				var cell struct {
					Ref  string `xml:"r,attr"`
					Type string `xml:"t,attr"`
					V    string `xml:"v"`
					IS   struct {
						Text string `xml:"t"`
					} `xml:"is"`
				}
				if err := decoder.DecodeElement(&cell, &node); err != nil {
					t.Fatal(err)
				}
				value := cell.V
				if cell.Type == "inlineStr" {
					value = cell.IS.Text
				}
				if column >= 3 || value != want[column] || cell.Ref != fmt.Sprintf("%c%d", 'A'+column, row) || column == 0 && cell.Type != "inlineStr" {
					t.Fatal("million XLSX changed exact text/amount/type/coordinate", row, column, cell, want)
				}
				column++
			}
		case xml.EndElement:
			if node.Name.Local == "row" && column != 3 {
				t.Fatal("million XLSX output omitted cells", row, column)
			}
		}
	}
	if row != count+1 {
		t.Fatal("incomplete million XLSX row coverage", row, count+1)
	}
	return sheet.UncompressedSize64
}

func TestExportResumeMillionActualCSVAndXLSXDownloadReadback(t *testing.T) {
	if os.Getenv("EXCELLENS_EXPORT_RESUME_SCALE") != "1" {
		t.Skip("set EXCELLENS_EXPORT_RESUME_SCALE=1 for actual million-source-row durable outputs and complete download readback")
	}
	const count = 1_000_000
	reports := make([]map[string]any, 0, 2)
	for _, format := range []string{"csv", "xlsx"} {
		t.Run(format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filterCheckpointCSV(t, count)
			before := printScaleHash(t, file)
			a := checkpointOpen(t, file)
			wb := a.currentWorkbook()
			meta := wb.Sheets[0]
			idx, err := wb.BuildIndex(meta, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			order, err := workbook.BuildRowOrder(context.Background(), filepath.Join(wb.CacheDir, "queries"), idx.RowCount(), func(ctx context.Context, add func(int) error) error {
				for position := idx.RowCount() - 1; position >= 0; position-- {
					if err := add(position); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			a.filterResults["million-output"] = &FilterResult{ID: "million-output", SheetPath: meta.Path, Rows: order, Sorted: true}
			req := exportRequest{Scope: "filtered", Sheet: meta.Name, FilterID: "million-output", Format: format, Mode: "values", Styles: true}
			selections, release := exportCheckpointPrepareOwned(t, a, req)
			store, lease := exportCheckpointCreate(t, a, req, selections)
			if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
				t.Fatal(err)
			}
			input, err := readExportCheckpointSelections(context.Background(), wb, lease)
			if err != nil {
				t.Fatal(err)
			}
			journal, err := newExportCheckpointJournal(context.Background(), wb, lease, input.selections)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			ctx, cancel := context.WithCancel(context.Background())
			err = excelExport.Resume(ctx, wb, input.selections, req.options(), pausingExportJournal{ResumeJournal: journal, after: 1, pause: cancel}, nil)
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			m := lease.Snapshot()
			confirmed := journal.State()
			_ = input.close()
			_ = lease.Close()
			_ = store.Close()
			release()
			a.Close()
			b := checkpointOpen(t, file)
			job := exportResumeJob(t, b, m)
			data := exportResumeDownload(t, b, job)
			var xmlBytes uint64
			if format == "csv" {
				verifyMillionResumedCSV(t, data, count)
			} else {
				xmlBytes = verifyMillionResumedXLSX(t, data, count)
			}
			final := checkpointManifest(t, m.ID)
			if !final.Complete || printScaleHash(t, file) != before {
				t.Fatal("million export did not complete durably or changed its source")
			}
			reports = append(reports, map[string]any{"format": format, "result": "PASS", "actualDataRows": count, "completeOutputRowsVerified": count + 1, "completeOutputCellsVerified": 3 * (count + 1), "beforeReopen": confirmed, "afterReopenJournalRecords": final.Records, "journalBytes": final.DataBytes, "journalBlocks": final.Blocks, "downloadBytes": len(data), "worksheetXMLBytes": xmlBytes, "sourceSHA256": before, "sourceUnchanged": true, "observedDurationMs": time.Since(started).Milliseconds(), "boundary": "actual million data rows plus header; complete HTTP download and independent exact value/coordinate checks; no peak memory, desktop acceptance or general speedup claim"})
		})
	}
	if t.Failed() {
		return
	}
	data, _ := json.MarshalIndent(map[string]any{"result": "PASS", "cases": reports}, "", "  ")
	if target := os.Getenv("EXCELLENS_EXPORT_RESUME_SCALE_REPORT"); target != "" {
		if err := os.WriteFile(target, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(data))
}
