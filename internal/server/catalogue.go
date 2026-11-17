package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Task state is protected by App.mu; a published snapshot is immutable. Listing
// uses source metadata only. Confirmation requires an already prepared index.
type catalogueTask = sourceMetadataTask[catalogueSnapshot]

type catalogueSnapshot struct {
	Value     workbook.Catalogue
	Positions map[string]int
	Bytes     int
}

type catalogueStartRequest struct {
	WorkbookKey string `json:"workbookKey"`
	Book        string `json:"book"`
	Mode        string `json:"mode"`
	Sheet       string `json:"sheet,omitempty"`
}

func (a *App) handleCatalogueStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req catalogueStartRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.CatalogueRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil || (req.Mode != "read" && req.Mode != "confirm") || (req.Mode == "read" && req.Sheet != "") {
		writeError(w, 400, "名称与表目录的读取参数无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "目录来源已切换，请重新打开目录")
		return
	}
	capability, supported := formats.Lookup(wb.Format)
	if !supported || !capability.CatalogueMetadata {
		writeError(w, 422, "此来源格式没有可读取的工作簿名称或表对象目录")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	var meta workbook.SheetMeta
	var idx *workbook.SheetIndex
	if req.Mode == "confirm" {
		var found bool
		meta, found = wb.FindSheet(req.Sheet)
		if !found {
			writeError(w, 404, "要确认的工作表不存在")
			return
		}
		idx = wb.GetCachedIndex(meta.Path)
		if idx == nil {
			writeError(w, 409, "请先打开并索引该工作表，再确认表对象")
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &catalogueTask{ID: randomID(), Mode: req.Mode, Status: "queued", Stage: "等待读取目录", Workbook: wb, Meta: meta, Index: idx, Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeError(w, 409, "工作簿已切换或正在维护缓存")
		return
	}
	if activeMetadataTasks(a.catalogueJobs) >= formats.CatalogueActiveTasks {
		a.mu.Unlock()
		cancel()
		writeError(w, 429, "目录任务正在读取，请等待或取消后重试")
		return
	}
	a.pruneCatalogueLocked("", formats.CatalogueTaskHistory-1)
	unpin := a.pinWorkbooks(wb)
	a.catalogueJobs[job.ID] = job
	a.mu.Unlock()
	go a.runCatalogue(job, unpin)
	writeJSON(w, map[string]string{"jobId": job.ID, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)})
}

func newCatalogueSnapshot(value workbook.Catalogue) (*catalogueSnapshot, error) {
	data, err := json.Marshal(value)
	if err != nil || len(data) > formats.CatalogueBytes || len(value.Items) > formats.CatalogueEntries {
		return nil, errors.New("名称与表目录超过本次读取容量")
	}
	snapshot := &catalogueSnapshot{Value: value, Positions: make(map[string]int, len(value.Items)), Bytes: len(data)}
	for i, item := range value.Items {
		if _, duplicate := snapshot.Positions[item.ID]; duplicate || item.ID == "" {
			return nil, errors.New("目录对象标识重复或缺失，不能确定唯一条目")
		}
		snapshot.Positions[item.ID] = i
	}
	return snapshot, nil
}

func (a *App) runCatalogue(job *catalogueTask, unpin func()) {
	runSourceMetadataTask(a, job, unpin, "目录", func() bool {
		return a.wb == job.Workbook && a.catalogueJobs[job.ID] == job
	}, func() { a.pruneCatalogueLocked(job.ID, formats.CatalogueTaskHistory) }, func(progress func(int64, int64, string)) (*catalogueSnapshot, error) {
		var value workbook.Catalogue
		var err error
		if job.Mode == "confirm" {
			value, err = job.Workbook.ConfirmCatalogue(job.ctx, job.Meta, job.Index, progress)
		} else {
			value, err = job.Workbook.ReadCatalogue(job.ctx)
		}
		if err != nil {
			return nil, err
		}
		return newCatalogueSnapshot(value)
	})
}

func (a *App) pruneCatalogueLocked(keep string, maximum int) {
	pruneMetadataTasks(a.catalogueJobs, keep, metadataTaskLimits{maximum, formats.CatalogueSnapshots, formats.CatalogueSnapshotBytes}, func(snapshot *catalogueSnapshot) int64 { return int64(snapshot.Bytes) })
}

func (a *App) clearCatalogueTasksLocked() []*catalogueTask {
	return clearMetadataTasks(a.catalogueJobs)
}

func waitCatalogueTasks(jobs []*catalogueTask) {
	waitMetadataTasks(jobs)
}

func (a *App) handleCatalogueStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.catalogueJobs[r.URL.Query().Get("id")]
	if job == nil || a.wb != job.Workbook || job.discarded {
		a.mu.RUnlock()
		writeError(w, 404, "目录任务已失效，请重新读取")
		return
	}
	payload := metadataTaskPayload(job)
	a.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) catalogueSnapshot(r *http.Request) (*workbook.Workbook, *catalogueSnapshot, error) {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	a.mu.RLock()
	job := a.catalogueJobs[r.URL.Query().Get("id")]
	if job == nil || job.Status != "done" || job.Snapshot == nil || a.wb != job.Workbook || job.discarded {
		a.mu.RUnlock()
		return nil, nil, errors.New("目录结果已失效，请重新读取")
	}
	wb, snapshot := job.Workbook, job.Snapshot
	a.mu.RUnlock()
	if err := wb.CheckSourceUnchanged(); err != nil {
		return nil, nil, err
	}
	a.mu.RLock()
	current := a.wb == wb && a.catalogueJobs[job.ID] == job && !job.discarded
	a.mu.RUnlock()
	if !current {
		return nil, nil, errors.New("目录来源已切换，请重新打开目录")
	}
	return wb, snapshot, nil
}

func (a *App) catalogueAction(w http.ResponseWriter, r *http.Request, clear bool) {
	id, err := metadataActionID(w, r, formats.CatalogueRequestBytes)
	if err != nil {
		return
	}
	a.mu.Lock()
	changed := metadataTaskAction(a.catalogueJobs, id, clear)
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"changed": changed})
}

func (a *App) handleCatalogueCancel(w http.ResponseWriter, r *http.Request) {
	a.catalogueAction(w, r, false)
}
func (a *App) handleCatalogueClear(w http.ResponseWriter, r *http.Request) {
	a.catalogueAction(w, r, true)
}

type catalogueListItem struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Scope        string `json:"scope"`
	Sheet        string `json:"sheet"`
	Status       string `json:"status"`
	Hidden       bool   `json:"hidden"`
	Shortened    bool   `json:"shortened"`
	UnknownScope bool   `json:"unknownScope"`
	Areas        int    `json:"areas"`
}

func catalogueListLabel(value string) (string, bool) {
	if utf8.RuneCountInString(value) <= formats.CatalogueLabelRunes {
		return value, false
	}
	return string([]rune(value)[:formats.CatalogueLabelRunes]) + "…", true
}

func (a *App) handleCatalogueItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	query, kind, sheet, scope := q.Get("q"), q.Get("kind"), q.Get("sheet"), q.Get("scope")
	offset := 0
	var err error
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
	}
	if err != nil || offset < 0 || offset > formats.CatalogueEntries || len(query) > formats.CatalogueSearchBytes || kind != "" && kind != "name" && kind != "table" || scope != "" && scope != "workbook" || scope == "workbook" && sheet != "" {
		writeError(w, 400, "目录筛选或分页参数无效")
		return
	}
	wb, snapshot, err := a.catalogueSnapshot(r)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	query = strings.ToLower(strings.TrimSpace(query))
	matched := 0
	items := make([]catalogueListItem, 0, formats.CataloguePageRows)
	for _, item := range snapshot.Value.Items {
		if r.Context().Err() != nil {
			return
		}
		if kind != "" && kind != item.Kind || sheet != "" && sheet != item.Sheet && sheet != item.Scope {
			continue
		}
		if scope == "workbook" && (item.Kind != "name" || item.Scope != "" || item.ScopeID != "") {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(item.Name+"\x00"+item.Scope+"\x00"+item.Sheet), query) {
			continue
		}
		if matched >= offset && len(items) < formats.CataloguePageRows {
			name, n := catalogueListLabel(item.Name)
			scope, s := catalogueListLabel(item.Scope)
			sheet, h := catalogueListLabel(item.Sheet)
			items = append(items, catalogueListItem{ID: item.ID, Kind: item.Kind, Name: name, Scope: scope, Sheet: sheet, Status: item.Status, Hidden: item.Hidden, Areas: len(item.Areas), Shortened: n || s || h, UnknownScope: item.Scope == "" && item.ScopeID != ""})
		}
		matched++
	}
	if offset > matched {
		writeError(w, 400, "目录分页位置超出当前结果，请从第一页重试")
		return
	}
	notices := make([]string, 0, len(snapshot.Value.Notices))
	for _, notice := range snapshot.Value.Notices {
		label, _ := catalogueListLabel(notice)
		notices = append(notices, label)
	}
	writeJSON(w, map[string]any{"items": items, "offset": offset, "previous": max(0, offset-formats.CataloguePageRows), "next": offset + len(items), "matched": matched, "total": len(snapshot.Value.Items), "complete": snapshot.Value.Complete, "notices": notices, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)})
}

func (a *App) handleCatalogueDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb, snapshot, err := a.catalogueSnapshot(r)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	pos, found := snapshot.Positions[r.URL.Query().Get("item")]
	if !found {
		writeError(w, 404, "目录条目不存在")
		return
	}
	writeJSON(w, map[string]any{"item": snapshot.Value.Items[pos], "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)})
}

func (a *App) handleCatalogueResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := r.URL.Query()
	area, err := strconv.Atoi(q.Get("area"))
	if err != nil || area < 0 {
		writeError(w, 400, "请选择有效的目录区域")
		return
	}
	wb, snapshot, err := a.catalogueSnapshot(r)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if q.Get("workbookKey") != wb.Fingerprint || q.Get("book") != sourceBookID(wb) {
		writeError(w, 409, "定位来源已切换，请重新打开目录")
		return
	}
	pos, found := snapshot.Positions[q.Get("item")]
	if !found {
		writeError(w, 404, "目录条目不存在")
		return
	}
	item := snapshot.Value.Items[pos]
	if item.Status != "ready" || area >= len(item.Areas) || item.Table != nil && !item.Table.Verified {
		writeError(w, 409, "此定义尚未确认或不能确定固定位置，请查看条目原因")
		return
	}
	writeJSON(w, map[string]any{"area": item.Areas[area], "workbookKey": wb.Fingerprint, "book": sourceBookID(wb)})
}
