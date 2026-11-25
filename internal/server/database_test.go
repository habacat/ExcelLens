package server

import (
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"excellens/internal/onlinedb"
	"excellens/internal/workbook"
)

func TestOnlineDatabasePostgreSQLTLS(t *testing.T) {
	port, err := strconv.Atoi(os.Getenv("EXCELLENS_QA_POSTGRES_PORT"))
	ca := os.Getenv("EXCELLENS_QA_POSTGRES_CA")
	if err != nil || ca == "" {
		t.Skip("requires the isolated PostgreSQL server and its CA certificate")
	}
	cert, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	cfg := onlinedb.Config{Engine: "postgres", Host: "127.0.0.1", Port: port, User: "lens_reader", Password: "fixture-reader-only", Database: "audit", Schema: "audit"}
	if out := reconcilePost(t, app, "/api/database/connect", cfg); out.Code != http.StatusBadRequest || !strings.Contains(out.Body.String(), "TLS 证书验证失败") {
		t.Fatal("unknown certificate was accepted or reason was hidden", out.Code, out.Body.String())
	}
	cfg.CA = string(cert)
	if list := decodeResponse[onlinedb.Catalogue](t, reconcilePost(t, app, "/api/database/connect", cfg)); len(list.Tables) != 3 {
		t.Fatal("validated TLS did not preserve the catalogue", list)
	}
}

func TestOnlineDatabaseRealReadOnlyWorkflow(t *testing.T) {
	for _, sample := range []struct{ engine, env, schema string }{
		{"postgres", "EXCELLENS_QA_POSTGRES_PORT", "audit"},
		{"mysql", "EXCELLENS_QA_MARIA_PORT", "audit"},
	} {
		t.Run(sample.engine, func(t *testing.T) {
			port, err := strconv.Atoi(os.Getenv(sample.env))
			if err != nil {
				t.Skip("set " + sample.env + " for the isolated fixture database")
			}
			isolateTestStorage(t)
			app, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			cfg := onlinedb.Config{Engine: sample.engine, Host: "127.0.0.1", Port: port, TLS: "plain", User: "lens_reader",
				Password: "fixture-reader-only", Database: "audit", Schema: sample.schema}
			metadata := decodeResponse[struct{ Engines []onlinedb.Engine }](t, featureCall(t, app, "GET", "/api/database/options", ""))
			if len(metadata.Engines) != 5 {
				t.Fatal(metadata.Engines)
			}
			catalogue := decodeResponse[onlinedb.Catalogue](t, reconcilePost(t, app, "/api/database/connect", cfg))
			if len(catalogue.Tables) != 3 || catalogue.Truncated {
				t.Fatal(catalogue)
			}
			get := func(name string) onlinedb.TableRef {
				t.Helper()
				for _, table := range catalogue.Tables {
					if table.Name == name && table.Schema == sample.schema {
						return table.TableRef
					}
				}
				t.Fatal("missing table", name, catalogue)
				return onlinedb.TableRef{}
			}
			job := submitDatabaseFixture(t, app, databaseImportRequest{cfg, []onlinedb.TableRef{get("ledger"), get("ledger_view")}})
			if result := job.status(); result.Status != "done" || result.Rows != 2 || result.WorkbookKey == "" {
				t.Fatal(result)
			}
			w := app.currentWorkbook()
			if w.Format != "db-online" || len(w.Sheets) != 2 || !w.SourceInfo.ReadOnly || w.Sheets[0].SourceName != "audit.ledger" {
				t.Fatal(w.SourceInfo, w.Sheets)
			}
			data := decodeResponse[struct{ Rows []workbook.RowData }](t, featureCall(t, app, "GET", "/api/sheet?sheet=ledger&row=2&rowWindow=1&limit=2&cols=7", ""))
			if len(data.Rows) != 2 || len(data.Rows[0].Cells) != 7 {
				t.Fatal(data.Rows)
			}
			row := data.Rows[0].Cells
			if row[0].Raw != "00001" || row[0].SourceType != "string" || row[1].Raw != "9007199254740993.3100" || row[1].Type != "n" ||
				row[2].SourceType != "null" || row[2].Display != "[NULL]" || row[3].Raw != "" || row[3].SourceType != "string" ||
				row[4].Raw != "2026-11-25" || row[4].Type != "d" || row[5].Raw != "0x0017ff" || row[5].SourceType != "binary" || row[6].Raw != "1" && row[6].Raw != "TRUE" {
				t.Fatal("source types or precision changed", row)
			}
			stats := decodeResponse[selectionStatResult](t, reconcilePost(t, app, "/api/selection/stats", map[string]any{
				"sheet": "ledger", "ranges": []map[string]string{{"start": "A2", "end": "B3"}},
			}))
			if stats.Number != 2 || stats.Sum != "9007199254740993.3" {
				t.Fatal(stats)
			}
			out := startRowExport(t, app, exportRequest{Scope: "sheet", Sheet: "ledger", Format: "jsonl", Mode: "values"})
			waitRowExport(t, out, "done")
			body, err := os.ReadFile(out.FilePath)
			if err != nil || !strings.Contains(string(body), `"sourceType":"null"`) || !strings.Contains(string(body), "9007199254740993.3100") {
				t.Fatal("exact source data unavailable in JSONL", err, string(body))
			}
			for _, path := range []string{w.Path, recentPath()} {
				body, err := os.ReadFile(path)
				if err != nil || strings.Contains(string(body), cfg.Password) || strings.Contains(string(body), cfg.User) {
					t.Fatal("credentials entered local metadata", path, err)
				}
			}
			rogue := submitDatabaseFixture(t, app, databaseImportRequest{cfg, []onlinedb.TableRef{{Schema: sample.schema, Name: "missing"}}})
			if rogue.status().Status != "error" || app.currentWorkbook() != w {
				t.Fatal("failed import replaced the previous workbook", rogue.status())
			}
			started := decodeResponse[databaseImportProgress](t, reconcilePost(t, app, "/api/database/import", databaseImportRequest{cfg, []onlinedb.TableRef{get("slow_view")}}))
			task := app.findDatabaseImport(started.ID)
			if task == nil {
				t.Fatal("missing cancellable task")
			}
			deadline := time.Now().Add(4 * time.Second)
			for task.status().Status == "connecting" && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if task.status().Status != "reading" {
				t.Fatal(task.status())
			}
			featureCall(t, app, "POST", "/api/database/cancel", `{"id":"`+started.ID+`"}`)
			select {
			case <-task.done:
			case <-time.After(6 * time.Second):
				t.Fatal("cancellation did not interrupt the SQL read")
			}
			if task.status().Status != "cancelled" || app.currentWorkbook() != w {
				t.Fatal("cancelled import replaced the previous workbook", task.status())
			}
			recent := decodeResponse[recentFile](t, featureCall(t, app, "GET", "/api/recent", ""))
			if len(recent.Items) != 1 || !recent.Items[0].Available || !recent.Items[0].Database {
				t.Fatal(recent)
			}
			featureCall(t, app, "POST", "/api/recent", `{"action":"open","id":"`+recent.Items[0].ID+`"}`)
			if opened := app.currentWorkbook(); opened == w || opened.Fingerprint != w.Fingerprint || cellValueServer(t, opened, opened.GetCachedIndex(opened.Sheets[0].Path), 2, 1) != "00001" {
				t.Fatal("recent snapshot did not reopen offline")
			}
		})
	}
}

func submitDatabaseFixture(t *testing.T, app *App, req databaseImportRequest) *databaseImportJob {
	t.Helper()
	start := decodeResponse[databaseImportProgress](t, reconcilePost(t, app, "/api/database/import", req))
	job := app.findDatabaseImport(start.ID)
	if job == nil {
		t.Fatal("missing database import")
	}
	select {
	case <-job.done:
	case <-time.After(30 * time.Second):
		t.Fatal("database import timed out")
	}
	got := decodeResponse[databaseImportProgress](t, featureCall(t, app, "GET", "/api/database/import?id="+url.QueryEscape(start.ID), ""))
	if got != job.status() {
		t.Fatal("status differs", got, job.status())
	}
	return job
}
