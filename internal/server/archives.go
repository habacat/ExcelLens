package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"excellens/internal/archives"
	"excellens/internal/workbook"
)

type archiveView struct {
	id                 string
	file               *archives.Archive
	depth              int
	rootPath, rootName string
	chain              []archiveHop
	parentNotice       string
}

type archiveHop = workbook.ArchiveStep
type archiveTask struct {
	mu                         sync.RWMutex
	id, status, stage, failure string
	doneBytes, totalBytes      int64
	result                     map[string]any
	cancel                     context.CancelFunc
	done                       chan struct{}
}

func (a *App) archiveSummary() map[string]any {
	a.archiveMu.Lock()
	defer a.archiveMu.Unlock()
	return summarizeArchive(a.archiveView)
}

func summarizeArchive(view *archiveView) map[string]any {
	if view == nil {
		return nil
	}
	f := view.file
	return map[string]any{"id": view.id, "name": f.Name, "format": f.Format, "count": len(f.Members), "total": f.Total, "depth": view.depth, "notice": archiveNotice(view)}
}

func archiveNotice(view *archiveView) string {
	return strings.TrimSpace(view.parentNotice + " " + view.file.Notice)
}

func decorateArchiveWorkbook(wb *workbook.Workbook, view *archiveView, member archives.Member) {
	wb.SourceInfo.ArchiveName, wb.SourceInfo.ArchiveMember = view.file.Name, member.Name
	wb.SourceInfo.ArchiveNotice = archiveNotice(view)
	if member.Incomplete {
		wb.SourceInfo.ArchiveNotice += " " + wb.SourceInfo.Notice
		name := filepath.Base(member.Name)
		ext := filepath.Ext(name)
		wb.DisplayName = strings.TrimSuffix(name, ext) + " [不完整片段]" + ext
	}
}

func (a *App) closeArchive() {
	a.archiveMu.Lock()
	view, job := a.archiveView, a.archiveTask
	a.archiveView, a.archiveTask = nil, nil
	if job != nil {
		job.cancel()
	}
	a.archiveMu.Unlock()
	if job != nil {
		<-job.done
	}
	if view != nil {
		_ = view.file.Close()
	}
}

func (a *App) openArchive(ctx context.Context, path, name string, depth int) error {
	file, err := archives.Open(ctx, path, name)
	if err != nil {
		return err
	}
	a.closeArchive()
	a.archiveMu.Lock()
	a.archiveView = &archiveView{id: randomID(), file: file, depth: depth, rootPath: path, rootName: name}
	a.archiveMu.Unlock()
	return nil
}

func (a *App) handleArchiveList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.archiveMu.Lock()
	view := a.archiveView
	if view == nil || view.id != r.URL.Query().Get("id") {
		a.archiveMu.Unlock()
		writeError(w, 404, "压缩包已关闭或切换，请重新选择")
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 200 {
		limit = 100
	}
	items := make([]archives.Member, 0, limit)
	total := 0
	for _, m := range view.file.Members {
		if query != "" && !strings.Contains(strings.ToLower(m.Name), query) {
			continue
		}
		if total >= offset && len(items) < limit {
			items = append(items, m)
		}
		total++
	}
	response := map[string]any{"archive": summarizeArchive(view), "items": items, "total": total, "offset": offset, "limit": limit}
	a.archiveMu.Unlock()
	writeJSON(w, response)
}

func (a *App) handleArchiveOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		Archive      string `json:"archive"`
		Member       int    `json:"member"`
		AllowPartial bool   `json:"allowPartial"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil {
		writeError(w, 400, "压缩包打开参数无效")
		return
	}
	a.archiveMu.Lock()
	view := a.archiveView
	if view == nil || view.id != input.Archive || input.Member < 0 || input.Member >= len(view.file.Members) {
		a.archiveMu.Unlock()
		writeError(w, 404, "压缩包成员不存在")
		return
	}
	if view.file.Members[input.Member].Incomplete && !input.AllowPartial {
		a.archiveMu.Unlock()
		writeError(w, 409, "该 TXT 不完整。请先勾选「仅查看现存片段」，或重新获取完整压缩包。")
		return
	}
	if old := a.archiveTask; old != nil {
		old.mu.RLock()
		running := old.status == "running"
		old.mu.RUnlock()
		if running {
			a.archiveMu.Unlock()
			writeError(w, 409, "正在读取另一成员，请先取消或等待完成")
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &archiveTask{id: randomID(), status: "running", stage: "正在准备读取", cancel: cancel, done: make(chan struct{})}
	a.archiveTask = job
	a.archiveMu.Unlock()
	go a.runArchiveOpen(ctx, view, input.Member, job)
	writeJSON(w, map[string]any{"jobId": job.id})
}

func (a *App) runArchiveOpen(ctx context.Context, view *archiveView, id int, job *archiveTask) {
	defer close(job.done)
	progress := func(done, total int64, stage string) {
		job.mu.Lock()
		job.doneBytes, job.totalBytes, job.stage = done, total, stage
		job.mu.Unlock()
	}
	var path string
	var member archives.Member
	var err error
	if view.file.Members[id].Incomplete {
		path, member, err = view.file.ExtractFragment(ctx, id, progress)
	} else {
		path, member, err = view.file.Extract(ctx, id, progress)
	}
	var wb *workbook.Workbook
	var nested *archives.Archive
	var result map[string]any
	var fileSize int64
	if err == nil {
		if member.Archive {
			if view.depth >= 4 {
				err = errors.New("嵌套压缩包超过 4 层，请单独打开需要的压缩包")
			} else {
				nested, err = archives.Open(ctx, path, member.Name)
			}
		} else {
			progress(0, 0, "正在识别成员文件并读取工作簿结构")
			if member.Incomplete {
				wb, err = workbook.OpenTextFragment(path)
			} else {
				wb, err = workbook.OpenWorkbook(path)
			}
			if err == nil {
				st, e := os.Stat(path)
				err = e
				if e == nil {
					fileSize = st.Size()
				}
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		a.archiveMu.Lock()
		if a.archiveView != view || a.archiveTask != job {
			err = context.Canceled
		} else if nested != nil {
			chain := append(append([]archiveHop(nil), view.chain...), archiveHop{Member: id, Name: member.Name})
			a.archiveView = &archiveView{id: randomID(), file: nested, depth: view.depth + 1, rootPath: view.rootPath, rootName: view.rootName, chain: chain, parentNotice: archiveNotice(view)}
			result = map[string]any{"archiveOnly": true, "opened": false, "archive": summarizeArchive(a.archiveView)}
		} else {
			wb.SetDisplayName(filepath.Base(member.Name))
			decorateArchiveWorkbook(wb, view, member)
			wb.ArchiveOrigin = &workbook.ArchiveOrigin{Path: view.rootPath, Name: view.rootName, Member: member.Name, MemberID: id, Chain: append([]archiveHop(nil), view.chain...), Partial: member.Incomplete}
			a.replaceWorkbook(wb)
			result = map[string]any{"opened": true, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "fileName": wb.DisplayName, "fileSize": fileSize, "sheets": wb.Sheets, "activeTab": wb.ActiveTab, "source": wb.SourceInfo, "archive": summarizeArchive(view)}
		}
		a.archiveMu.Unlock()
		if err == nil && nested != nil {
			_ = view.file.Close()
		}
		if err == nil && wb != nil {
			a.recordRecent(wb)
		}
	}
	if err != nil && nested != nil {
		_ = nested.Close()
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	job.result = result
	if err != nil {
		job.status = "error"
		job.failure = err.Error()
		if errors.Is(err, context.Canceled) {
			job.status = "cancelled"
			job.failure = "读取已取消"
		}
	} else {
		job.status = "done"
		job.stage = "成员文件已打开"
	}
}

func (a *App) handleArchiveJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	a.archiveMu.Lock()
	job := a.archiveTask
	a.archiveMu.Unlock()
	if job == nil || job.id != r.URL.Query().Get("id") {
		writeError(w, 404, "压缩包读取任务不存在")
		return
	}
	if r.Method == http.MethodPost {
		job.cancel()
	}
	job.mu.RLock()
	payload := map[string]any{"id": job.id, "status": job.status, "stage": job.stage, "doneBytes": job.doneBytes, "totalBytes": job.totalBytes, "error": job.failure, "result": job.result}
	job.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) archiveRecent(wb *workbook.Workbook, item *recentEntry) {
	if origin := wb.ArchiveOrigin; origin != nil {
		item.ArchivePath, item.ArchiveName, item.ArchiveMember, item.ArchiveEntry = origin.Path, origin.Name, origin.Member, origin.MemberID
		item.ArchiveChain = append([]archiveHop(nil), origin.Chain...)
		item.ArchivePartial = origin.Partial
		return
	}
	if wb.SourceInfo.ArchiveName == "" {
		return
	}
	a.archiveMu.Lock()
	defer a.archiveMu.Unlock()
	if view := a.archiveView; view != nil {
		item.ArchivePath, item.ArchiveName = view.rootPath, view.rootName
		item.ArchiveChain = append([]archiveHop(nil), view.chain...)
		item.ArchiveMember = wb.SourceInfo.ArchiveMember
		for _, m := range view.file.Members {
			if m.Name == wb.SourceInfo.ArchiveMember {
				item.ArchiveEntry = m.ID
				break
			}
		}
	}
}

func (a *App) reopenArchiveRecent(ctx context.Context, item recentEntry) error {
	if err := a.openArchive(ctx, item.ArchivePath, item.ArchiveName, 0); err != nil {
		return err
	}
	a.archiveMu.Lock()
	view := a.archiveView
	a.archiveMu.Unlock()
	for _, hop := range item.ArchiveChain {
		id, err := findArchiveMember(view.file, hop.Member, hop.Name)
		if err != nil {
			return err
		}
		path, member, err := view.file.Extract(ctx, id, nil)
		if err != nil {
			return err
		}
		nested, err := archives.Open(ctx, path, member.Name)
		if err != nil {
			return err
		}
		next := &archiveView{id: randomID(), file: nested, depth: view.depth + 1, rootPath: view.rootPath, rootName: view.rootName, chain: append(append([]archiveHop(nil), view.chain...), hop), parentNotice: archiveNotice(view)}
		a.archiveMu.Lock()
		a.archiveView = next
		a.archiveMu.Unlock()
		_ = view.file.Close()
		view = next
	}
	id, err := findArchiveMember(view.file, item.ArchiveEntry, item.ArchiveMember)
	if err != nil {
		return err
	}
	var path string
	var m archives.Member
	if view.file.Members[id].Incomplete && item.ArchivePartial {
		path, m, err = view.file.ExtractFragment(ctx, id, nil)
	} else {
		path, m, err = view.file.Extract(ctx, id, nil)
	}
	if err != nil {
		return err
	}
	var wb *workbook.Workbook
	if m.Incomplete {
		wb, err = workbook.OpenTextFragment(path)
	} else {
		wb, err = workbook.OpenWorkbook(path)
	}
	if err != nil {
		return fmt.Errorf("读取压缩包成员失败：%w", err)
	}
	wb.SetDisplayName(item.Name)
	decorateArchiveWorkbook(wb, view, m)
	wb.ArchiveOrigin = &workbook.ArchiveOrigin{Path: item.ArchivePath, Name: item.ArchiveName, Member: m.Name, MemberID: id, Chain: append([]archiveHop(nil), view.chain...), Partial: m.Incomplete}
	a.replaceWorkbook(wb)
	return nil
}

func findArchiveMember(a *archives.Archive, id int, name string) (int, error) {
	if id >= 0 && id < len(a.Members) && (name == "" || a.Members[id].Name == name) {
		return id, nil
	}
	for _, m := range a.Members {
		if m.Name == name {
			return m.ID, nil
		}
	}
	return 0, errors.New("压缩包成员已移动或更名，请从压缩包目录重新选择")
}
