package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"excellens/internal/formats"
	"excellens/internal/onlinedb"
	"excellens/internal/workbook"
)

var databaseSlots = make(chan struct{}, 2)

type databaseImportController struct {
	databaseMu  sync.Mutex
	databaseJob *databaseImportJob
}

type databaseImportProgress struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Name        string `json:"name,omitempty"`
	Table       int    `json:"table"`
	Tables      int    `json:"tables"`
	Rows        int    `json:"rows"`
	Error       string `json:"error,omitempty"`
	WorkbookKey string `json:"workbookKey,omitempty"`
}

type databaseImportJob struct {
	mu       sync.Mutex
	progress databaseImportProgress
	done     chan struct{}
	cancel   context.CancelFunc
}

func (j *databaseImportJob) status() databaseImportProgress {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.progress
}

func (a *App) handleDatabaseOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, map[string]any{"engines": onlinedb.Engines(), "tablesLimit": formats.DatabaseSelectedTables, "certificateBytes": formats.DatabaseCertificateBytes})
}

func decodeDatabaseRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, formats.DatabaseRequestBytes))
	d.DisallowUnknownFields()
	var trailing any
	if d.Decode(target) != nil || d.Decode(&trailing) != io.EOF {
		writeError(w, http.StatusBadRequest, "数据库请求参数无效或超限")
		return false
	}
	return true
}

func takeDatabaseSlot(w http.ResponseWriter) bool {
	select {
	case databaseSlots <- struct{}{}:
		return true
	default:
		writeError(w, http.StatusTooManyRequests, "数据库连接繁忙，请等待当前读取完成")
		return false
	}
}

func (a *App) handleDatabaseConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var cfg onlinedb.Config
	if !decodeDatabaseRequest(w, r, &cfg) || !takeDatabaseSlot(w) {
		return
	}
	defer func() { <-databaseSlots }()
	ctx := r.Context()
	connection, err := onlinedb.Open(ctx, cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer connection.Close()
	list, err := connection.Tables(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, struct {
		onlinedb.Catalogue
		Name string `json:"name"`
	}{list, connection.Label()})
}

type databaseImportRequest struct {
	Connection onlinedb.Config     `json:"connection"`
	Tables     []onlinedb.TableRef `json:"tables"`
}

func (a *App) handleDatabaseImport(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		job := a.findDatabaseImport(r.URL.Query().Get("id"))
		if job == nil {
			writeError(w, http.StatusNotFound, "找不到当前页面的数据库读取任务")
			return
		}
		writeJSON(w, job.status())
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input databaseImportRequest
	if !decodeDatabaseRequest(w, r, &input) {
		return
	}
	if len(input.Tables) == 0 || len(input.Tables) > formats.DatabaseSelectedTables {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("请选择 1 至 %d 张表", formats.DatabaseSelectedTables))
		return
	}
	expected := a.currentWorkbook()
	a.archiveMu.Lock()
	expectedArchive := a.archiveView
	a.archiveMu.Unlock()
	a.databaseMu.Lock()
	if old := a.databaseJob; old != nil {
		select {
		case <-old.done:
		default:
			a.databaseMu.Unlock()
			writeError(w, http.StatusConflict, "本页面已有数据库读取任务")
			return
		}
	}
	if !takeDatabaseSlot(w) {
		a.databaseMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	queued := databaseImportProgress{ID: randomID(), Status: "connecting", Tables: len(input.Tables)}
	job := &databaseImportJob{progress: queued, cancel: cancel, done: make(chan struct{})}
	a.databaseJob = job
	a.databaseMu.Unlock()
	go func() {
		defer close(job.done)
		defer cancel()
		defer func() { <-databaseSlots }()
		err := a.runDatabaseImport(ctx, job, input, expected, expectedArchive)
		job.mu.Lock()
		defer job.mu.Unlock()
		switch {
		case errors.Is(err, context.Canceled):
			job.progress.Status = "cancelled"
		case err != nil:
			job.progress.Status, job.progress.Error = "error", err.Error()
		default:
			job.progress.Status = "done"
		}
	}()
	writeJSON(w, queued)
}

func (a *App) findDatabaseImport(id string) *databaseImportJob {
	a.databaseMu.Lock()
	defer a.databaseMu.Unlock()
	if job := a.databaseJob; job != nil && job.status().ID == id {
		return job
	}
	return nil
}

func (a *App) handleDatabaseCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request struct {
		ID string `json:"id"`
	}
	if !decodeDatabaseRequest(w, r, &request) {
		return
	}
	job := a.findDatabaseImport(request.ID)
	if job == nil {
		writeError(w, http.StatusNotFound, "找不到该数据库读取任务")
		return
	}
	job.mu.Lock()
	if job.progress.Status == "connecting" || job.progress.Status == "reading" {
		job.cancel()
	}
	status := job.progress
	job.mu.Unlock()
	writeJSON(w, status)
}

func (a *App) closeDatabaseImport() {
	a.databaseMu.Lock()
	job := a.databaseJob
	a.databaseJob = nil
	a.databaseMu.Unlock()
	if job != nil {
		job.cancel()
		<-job.done
	}
}

func (a *App) runDatabaseImport(ctx context.Context, job *databaseImportJob, request databaseImportRequest, expected *workbook.Workbook, expectedArchive *archiveView) error {
	connection, err := onlinedb.Open(ctx, request.Connection)
	if err != nil {
		return err
	}
	defer connection.Close()
	list, err := connection.Tables(ctx)
	if err != nil {
		return err
	}
	available := make(map[onlinedb.TableRef]onlinedb.Table, len(list.Tables))
	for _, table := range list.Tables {
		available[table.TableRef] = table
	}
	selected := make([]workbook.DatabaseSnapshotTable, 0, len(request.Tables))
	for _, ref := range request.Tables {
		table, ok := available[ref]
		if !ok {
			return errors.New("所选数据表重复、已变化或不可读取，请重新连接")
		}
		selected = append(selected, workbook.DatabaseSnapshotTable{Schema: table.Schema, Name: table.Name, Kind: table.Kind})
		delete(available, ref)
	}
	source := connection.Source()
	snapshot, err := workbook.StartDatabaseSnapshot(connection.Label(), workbook.DatabaseSnapshotSource{
		Engine: source.Engine, Protocol: source.Protocol, Host: source.Host, Port: source.Port,
		Database: source.Database, TLS: source.TLS,
	}, selected)
	if err != nil {
		return err
	}
	defer snapshot.Discard()
	for i, ref := range request.Tables {
		job.mu.Lock()
		job.progress.Status, job.progress.Table, job.progress.Name, job.progress.Rows = "reading", i+1, ref.Schema+"."+ref.Name, 0
		job.mu.Unlock()
		stream := func(begin func(workbook.DatabaseTableDefinition) error, yield func([]workbook.DatabaseCell) error) error {
			var converted []workbook.DatabaseCell
			return connection.StreamTable(ctx, ref, func(read onlinedb.ReadDefinition) error {
				def := workbook.DatabaseTableDefinition{Query: read.Query, Ordering: "unspecified", Columns: make([]workbook.DatabaseColumn, len(read.Columns))}
				for i, col := range read.Columns {
					def.Columns[i] = workbook.DatabaseColumn{Name: col.Name, Type: col.Type, Nullable: col.Nullable, Precision: col.Precision, Scale: col.Scale}
				}
				return begin(def)
			}, func(row []onlinedb.Value) error {
				if len(converted) != len(row) {
					converted = make([]workbook.DatabaseCell, len(row))
				}
				for col, value := range row {
					converted[col] = workbook.DatabaseCell{Kind: value.Kind, Raw: value.Raw}
				}
				return yield(converted)
			})
		}
		if err := snapshot.WriteTable(ctx, stream, func(rows int) {
			job.mu.Lock()
			job.progress.Rows = rows
			job.mu.Unlock()
		}); err != nil {
			return err
		}
	}
	job.mu.Lock()
	if err := ctx.Err(); err != nil {
		job.mu.Unlock()
		return err
	}
	job.progress.Status = "opening"
	job.mu.Unlock()
	root := a.rootApp()
	// Full cache reset and restore can cancel or gate imports. Do not wait
	// for this lock while the reset is joining the import from another page.
	if !root.cacheResetMu.TryRLock() {
		return errors.New("本地数据管理正在进行，数据库快照尚未保存")
	}
	defer root.cacheResetMu.RUnlock()
	if root.restoreState.pending() {
		return errors.New("请先完成恢复事务")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	wb, err := snapshot.Commit(ctx)
	if err != nil {
		return err
	}
	a.archiveMu.Lock()
	if a.archiveView != expectedArchive || !a.installWorkbook(wb, expected, true) {
		a.archiveMu.Unlock()
		return errors.New("快照已保存到「数据库快照」。当前工作簿已切换")
	}
	view, archiveJob := a.detachArchiveLocked()
	a.archiveMu.Unlock()
	finishClosedArchive(view, archiveJob)
	a.recordRecent(wb)
	job.mu.Lock()
	job.progress.WorkbookKey = wb.Fingerprint
	job.mu.Unlock()
	return nil
}
