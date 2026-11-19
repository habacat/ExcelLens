package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func recordOpen(t *testing.T, file string) (*App, workbook.SheetMeta) {
	t.Helper()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	meta := a.currentWorkbook().Sheets[0]
	if _, err := a.currentWorkbook().BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	return a, meta
}

func assertRecordCommonRead(t *testing.T, a *App, meta workbook.SheetMeta, req recordRequest) recordResponse {
	t.Helper()
	r := recordOK(t, a, req)
	wb := a.currentWorkbook()
	idx := wb.GetCachedIndex(meta.Path)
	rows, err := wb.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{req.Row}, req.FirstColumn, req.FirstColumn+r.Columns-1, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, cell := range rows[0].Cells {
		if !reflect.DeepEqual(r.Fields[cell.Col-req.FirstColumn].Cell, cell) {
			t.Fatal("adapter data changed in card", wb.Format, cell.Ref)
		}
	}
	return r
}

func TestRecordUsesRealTextHTMLAndBIFFAdapters(t *testing.T) {
	for _, tc := range []struct{ name, format, body string }{
		{"source.csv", "csv", "编号,金额\n001,9007199254740993.31\n002,-2.25\n"},
		{"source.tsv", "tsv", "编号\t金额\n001\t9007199254740993.31\n002\t-2.25\n"},
		{"source.txt", "txt", "编号\t金额\n001\t9007199254740993.31\n002\t-2.25\n"},
		{"html.xls", "xls-html", "<html><table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>9007199254740993.31</td></tr><tr><td>002</td><td>-2.25</td></tr></table></html>"},
		{"native.xls", "xls", ""},
	} {
		t.Run(tc.format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), tc.name)
			if tc.body != "" {
				if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				printBIFFSource(t, file, 4)
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			if a.currentWorkbook().Format != tc.format {
				t.Fatal("wrong real adapter", a.currentWorkbook().Format)
			}
			req := recordSource(t, a, meta.Name, rowViewRequest{})
			req.Columns = 2
			r := assertRecordCommonRead(t, a, meta, req)
			identifier, amount := "001", "9007199254740993.31"
			if tc.format == "xls" {
				identifier, amount = "0000000002", "2.5"
			}
			if r.Fields[0].Cell.Raw != identifier || r.Fields[1].Cell.Raw != amount {
				t.Fatal("source text/amount changed", r.Fields)
			}
			if tc.format == "xls" && (r.Fields[0].Cell.Type != "inlineStr" || r.Fields[1].Cell.Type != "n") {
				t.Fatal("native types changed", r.Fields)
			}
			if printScaleHash(t, file) != before {
				t.Fatal("adapter source changed")
			}
		})
	}
}

func TestRecordSourceCapacityIsCheckedBeforeOpeningTheRowCache(t *testing.T) {
	idx := &workbook.SheetIndex{XMLPath: filepath.Join(t.TempDir(), "absent.xml"), XMLSize: formats.RecordCardSourceBytes + 1, MaxRow: 2, MaxCol: 1, ResidentRows: []workbook.RowOffset{{Number: 2, Start: 0, End: formats.RecordCardSourceBytes + 1}}}
	_, err := readRecord(context.Background(), &workbook.Workbook{}, idx, recordRequest{Row: 2, FirstColumn: 1, Columns: 1})
	if !errors.Is(err, errRecordCapacity) {
		t.Fatal("oversized row reached cache IO", err)
	}
	idx.ResidentRows[0].End = idx.XMLSize + 1
	_, err = readRecord(context.Background(), &workbook.Workbook{}, idx, recordRequest{Row: 2, FirstColumn: 1, Columns: 1})
	if err == nil || errors.Is(err, errRecordCapacity) || !strings.Contains(err.Error(), "缓存范围") {
		t.Fatal("bad descriptor disguised as capacity", err)
	}
}

func TestRecordReleasedIndexAndMaintenanceNeverPublishOldData(t *testing.T) {
	a, _ := recordFixture(t, 33, "")
	req := recordSource(t, a, "First", rowViewRequest{})
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	wb.ClearIndexes()
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	if response := recordCall(t, a, req); response.Code != 409 {
		t.Fatal("replaced exact index served stale card", response.Code, response.Body.String())
	}
	req = recordSource(t, a, "First", rowViewRequest{})
	a.mu.Lock()
	a.maintenance = true
	a.mu.Unlock()
	response := recordCall(t, a, req)
	a.mu.Lock()
	a.maintenance = false
	a.mu.Unlock()
	if response.Code != 409 || strings.Contains(response.Body.String(), `"fields"`) {
		t.Fatal("maintenance published record", response.Code, response.Body.String())
	}
}

func TestRecordRequestsRemainBoundToTheirBrowserSession(t *testing.T) {
	a, file := recordFixture(t, 33, "")
	data, _ := os.ReadFile(file)
	other := filepath.Join(t.TempDir(), "same-content.xlsx")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	sessionA, err := a.OpenPathForLaunch(file)
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := a.OpenPathForLaunch(other)
	if err != nil {
		t.Fatal(err)
	}
	a.sessionsMu.Lock()
	viewerA := a.sessions[sessionA].app
	viewerB := a.sessions[sessionB].app
	a.sessionsMu.Unlock()
	for _, viewer := range []*App{viewerA, viewerB} {
		wb := viewer.currentWorkbook()
		meta, _ := wb.FindSheet("First")
		if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	req := recordSource(t, viewerA, "First", rowViewRequest{})
	encoded, _ := json.Marshal(req)
	if response := sessionCall(t, a, sessionA, "POST", "/api/record", string(encoded)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response := sessionCall(t, a, sessionB, "POST", "/api/record", string(encoded)); response.Code != 409 {
		t.Fatal("other page accepted equal-content source", response.Code, response.Body.String())
	}
}
