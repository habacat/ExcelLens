package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

func exportCheckpointPrepare(t *testing.T, a *App, req exportRequest) []excelExport.Selection {
	selections, _ := exportCheckpointPrepareOwned(t, a, req)
	return selections
}

func exportCheckpointPrepareOwned(t *testing.T, a *App, req exportRequest) ([]excelExport.Selection, func()) {
	t.Helper()
	wb := a.currentWorkbook()
	rows, err := a.captureExportRows(wb, req)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(rows.close) }
	t.Cleanup(release)
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	selections, err := a.prepareExportSelections(context.Background(), wb, req, rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	return selections, release
}

func exportCheckpointCreate(t *testing.T, a *App, req exportRequest, selections []excelExport.Selection) (*checkpoint.Store, *checkpoint.Lease) {
	t.Helper()
	store, err := openTaskCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	lease, err := createExportCheckpoint(context.Background(), store, a.currentWorkbook(), req, selections)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return store, lease
}

func exportCheckpointOutput(t *testing.T, wb *workbook.Workbook, selections []excelExport.Selection, opts excelExport.Options) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := excelExport.Write(context.Background(), &out, wb, selections, opts, nil); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func exportCheckpointParts(t *testing.T, data []byte) map[string]string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	parts := make(map[string]string)
	for _, part := range z.File {
		f, err := part.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || parts[part.Name] != "" {
			t.Fatal("broken or duplicate archive part", part.Name, err)
		}
		parts[part.Name] = string(body)
	}
	return parts
}

// These are two independent exports of the same frozen selection, not two
// encodings of one output task. Each has its own valid creation timestamp.
// Validate that timestamp against the observed operation window; compare all
// remaining manifest fields and every CSV/archive member without exclusions.
func exportCheckpointManifestTime(t *testing.T, parts map[string]string, started, finished time.Time) {
	t.Helper()
	const name = "_ExcelLens_manifest.json"
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(parts[name]), &fields); err != nil {
		t.Fatal("invalid CSV archive manifest", err)
	}
	var value string
	if err := json.Unmarshal(fields["createdAt"], &value); err != nil {
		t.Fatal("missing/invalid archive creation time", err)
	}
	created, err := time.Parse(time.RFC3339, value)
	if err != nil || created.Before(started.Truncate(time.Second)) || created.After(finished.Truncate(time.Second)) {
		t.Fatal("archive creation time is outside the actual export window", value, started, finished, err)
	}
	fields["createdAt"] = json.RawMessage(`"validated against actual export window"`)
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	parts[name] = string(data)
}

func TestExportCheckpointSixFormatsAndContentModesReopenExactFrozenInputs(t *testing.T) {
	for _, format := range []string{"csv", "tsv", "jsonl", "html", "xlsx", "zipcsv"} {
		for _, mode := range []string{"values", "display", "formula"} {
			t.Run(format+"-"+mode, func(t *testing.T) {
				isolateTestStorage(t)
				a, file := rowViewFixture(t)
				before := printScaleHash(t, file)
				wb := a.currentWorkbook()
				meta, _ := wb.FindSheet("First")
				idx := wb.GetCachedIndex(meta.Path)
				a.filterResults["ephemeral-export-order"] = &FilterResult{ID: "ephemeral-export-order", Sheet: "First", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
				viewID := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{FilterID: "ephemeral-export-order"}), "done")
				req := exportRequest{Scope: "visible", Sheet: "First", FilterID: "ephemeral-export-order", ViewID: viewID, ExcludeThroughRow: 6, Format: format, Mode: mode, BOM: true, ProtectCSV: true, Styles: true}
				if format == "xlsx" && mode == "formula" {
					req.Scope, req.FilterID, req.ViewID = "sheet", "", ""
				}
				selections, release := exportCheckpointPrepareOwned(t, a, req)
				exportStarted := time.Now()
				want := exportCheckpointOutput(t, wb, selections, req.options())
				store, lease := exportCheckpointCreate(t, a, req, selections)
				if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
					t.Fatal(err)
				}
				m := lease.Snapshot()
				if m.Complete || m.Stage != "ready" || bytes.Contains(m.Definition.Payload, []byte("ephemeral-export-order")) || bytes.Contains(m.Definition.Payload, []byte(viewID)) {
					t.Fatal("input snapshot became a complete export or retained a transient ID", m)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
				featureCall(t, a, "POST", "/api/filter/clear", `{"id":"ephemeral-export-order"}`)
				featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+viewID+`"}`)
				// Drop the original view, selection owner and process-local caches.
				release()
				a.Close()
				b := checkpointOpen(t, file)
				lease, err := store.Acquire(context.Background(), m.ID, m.DefinitionHash)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				restored, err := readExportCheckpointSelections(context.Background(), b.currentWorkbook(), lease)
				if err != nil {
					t.Fatal(err)
				}
				defer restored.close()
				got := exportCheckpointOutput(t, b.currentWorkbook(), restored.selections, req.options())
				if format == "xlsx" || format == "zipcsv" {
					gotParts, wantParts := exportCheckpointParts(t, got), exportCheckpointParts(t, want)
					if format == "zipcsv" {
						finished := time.Now()
						exportCheckpointManifestTime(t, gotParts, exportStarted, finished)
						exportCheckpointManifestTime(t, wantParts, exportStarted, finished)
					}
					if !reflect.DeepEqual(gotParts, wantParts) {
						for name, body := range gotParts {
							if body != wantParts[name] {
								t.Logf("different archive part %s: got=%q want=%q", name, body, wantParts[name])
							}
						}
						t.Fatal("restored archive changed styles, coordinates, values or types")
					}
				} else if !bytes.Equal(got, want) {
					t.Fatal("restored export changed bytes", string(got), string(want))
				}
				if format == "csv" && mode == "values" && string(got) != "\xef\xbb\xbf001,1.10\r\n,9007199254740993.31\r\n,\r\n,7.70\r\n" {
					t.Fatal("frozen blanks/leading zeroes/exact decimal changed", string(got))
				}
				if printScaleHash(t, file) != before {
					t.Fatal("source changed")
				}
			})
		}
	}
}

func TestExportCheckpointPhysicalSelectionSheetAndAllKeepScopeWithoutRowExpansion(t *testing.T) {
	for _, scope := range []string{"selection", "sheet", "all"} {
		t.Run(scope, func(t *testing.T) {
			isolateTestStorage(t)
			a, _ := rowViewFixture(t)
			wb := a.currentWorkbook()
			req := exportRequest{Scope: scope, Sheet: "First", Start: "B7", End: "A2", Format: "xlsx", Mode: "values", Styles: true, IncludeHidden: true}
			selections := exportCheckpointPrepare(t, a, req)
			want := exportCheckpointOutput(t, wb, selections, req.options())
			_, lease := exportCheckpointCreate(t, a, req, selections)
			if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
				t.Fatal(err)
			}
			if m := lease.Snapshot(); m.Blocks != 0 || m.Records != 0 || m.DataBytes != 0 {
				t.Fatal("physical span was expanded to per-row storage", m)
			}
			restored, err := readExportCheckpointSelections(context.Background(), wb, lease)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.close()
			got := exportCheckpointOutput(t, wb, restored.selections, req.options())
			if !reflect.DeepEqual(exportCheckpointParts(t, got), exportCheckpointParts(t, want)) {
				t.Fatal("scope or compact coordinates changed")
			}
		})
	}
}

func TestExportCheckpointInterruptedCaptureNeverInfersMissingView(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	req := exportRequest{Scope: "sheet", Sheet: "First", Format: "csv", Mode: "values"}
	selections := exportCheckpointPrepare(t, a, req)
	store, lease := exportCheckpointCreate(t, a, req, selections)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := captureExportCheckpoint(ctx, wb, lease, selections); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled input capture was accepted", err)
	}
	m := lease.Snapshot()
	_ = lease.Close()
	lease, err := store.Acquire(context.Background(), m.ID, m.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if got, err := readExportCheckpointSelections(context.Background(), wb, lease); got != nil || !errors.Is(err, errExportCaptureIncomplete) {
		t.Fatal("unfinished input capture silently became resumable", got, err)
	}
}

func TestExportCheckpointRejectsChangedSourceIndexAndDurablePayload(t *testing.T) {
	for _, change := range []string{"path", "source", "cache", "missing-cache", "blocks", "semantics", "cursor"} {
		t.Run(change, func(t *testing.T) {
			isolateTestStorage(t)
			file := filterCheckpointCSV(t, 15)
			a := checkpointOpen(t, file)
			wb := a.currentWorkbook()
			meta := wb.Sheets[0]
			idx, err := wb.BuildIndex(meta, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			a.filterResults["order"] = &FilterResult{ID: "order", SheetPath: meta.Path, Rows: rowOrderFixture(t, idx.RowCount(), 15, 1, 0, 4)}
			req := exportRequest{Scope: "filtered", Sheet: meta.Name, FilterID: "order", Format: "csv", Mode: "values"}
			selections := exportCheckpointPrepare(t, a, req)
			store, lease := exportCheckpointCreate(t, a, req, selections)
			if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
				t.Fatal(err)
			}
			m := lease.Snapshot()
			_ = lease.Close()
			if change == "semantics" || change == "cursor" {
				def := m.Definition
				cursor := m.Cursor
				if change == "semantics" {
					def.Semantics++
				} else {
					cursor = []byte(`{"snapshot":true,"frozen":"-1"}`)
				}
				lease, err := store.Create(context.Background(), def, "ready", cursor)
				if err != nil {
					t.Fatal(err)
				}
				defer lease.Close()
				if got, err := readExportCheckpointSelections(context.Background(), wb, lease); got != nil || err == nil {
					t.Fatal("unknown semantics or bad cursor published rows", got, err)
				}
				return
			}
			if change == "blocks" {
				base, _ := os.UserConfigDir()
				path := filepath.Join(base, "ExcelLens", "task-checkpoints", m.ID, "blocks")
				data, _ := os.ReadFile(path)
				data[len(data)-1] ^= 1
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if bad, err := store.Acquire(context.Background(), m.ID, m.DefinitionHash); bad != nil || !errors.Is(err, checkpoint.ErrCorrupt) {
					t.Fatal("corrupt durable rows were accepted", bad, err)
				}
				return
			}
			path := file
			if change == "cache" || change == "missing-cache" {
				path = idx.XMLPath
			}
			if change == "missing-cache" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				data, _ := os.ReadFile(path)
				info, _ := os.Stat(path)
				if change == "path" {
					path = filepath.Join(t.TempDir(), "identical.csv")
				} else {
					data = []byte(strings.Replace(string(data), "keep", "KEEP", 1))
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				if change == "path" {
					wb = checkpointOpen(t, path).currentWorkbook()
				}
			}
			lease, err = store.Acquire(context.Background(), m.ID, m.DefinitionHash)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if got, err := readExportCheckpointSelections(context.Background(), wb, lease); got != nil || err == nil {
				t.Fatal("changed source/cache was published", change, got, err)
			}
		})
	}
}

func TestExportCheckpointDefinitionChangesForEveryOutputOptionAndExactRowOrder(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	req := exportRequest{Scope: "sheet", Sheet: "First", Format: "csv", Mode: "values"}
	selections := exportCheckpointPrepare(t, a, req)
	definition, err := exportTaskDefinition(context.Background(), wb, req, selections)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := checkpoint.DefinitionHash(definition)
	for _, change := range []func(*exportRequest){
		func(r *exportRequest) { r.BOM = true }, func(r *exportRequest) { r.ProtectCSV = true },
		func(r *exportRequest) { r.Styles = true }, func(r *exportRequest) { r.Mode = "display" },
		func(r *exportRequest) { r.Mode = "formula" }, func(r *exportRequest) { r.Format = "tsv" },
		func(r *exportRequest) { r.IncludeHidden = true },
	} {
		changed := req
		change(&changed)
		def, err := exportTaskDefinition(context.Background(), wb, changed, selections)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := checkpoint.DefinitionHash(def)
		if got == want {
			t.Fatal("output option missing from immutable identity", changed)
		}
	}
	var spec exportCheckpointSpec
	if err := json.Unmarshal(definition.Payload, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Options != req.options() || spec.Sheets[0].Rows != 7 || spec.Sheets[0].RowHash != "" {
		t.Fatal("implicit source span was changed", spec)
	}
	ordered := selections[0]
	ordered.Filtered, ordered.Compact, ordered.RowNumbers = true, true, []int{7, 1, 6}
	req.Scope = "filtered"
	first, err := exportTaskDefinition(context.Background(), wb, req, []excelExport.Selection{ordered})
	if err != nil {
		t.Fatal(err)
	}
	ordered.RowNumbers = []int{1, 7, 6}
	second, err := exportTaskDefinition(context.Background(), wb, req, []excelExport.Selection{ordered})
	if err != nil {
		t.Fatal(err)
	}
	x, _ := checkpoint.DefinitionHash(first)
	y, _ := checkpoint.DefinitionHash(second)
	if x == y {
		t.Fatal("equal-length reordered rows shared an immutable identity")
	}
}
