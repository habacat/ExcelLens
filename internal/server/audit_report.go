package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

type auditCSV struct {
	out  *csv.Writer
	rows int64
}

// Count actual output bytes, including CSV quoting, UTF-8 and ZIP metadata.
// A failed write aborts the existing atomic export before it exposes a ticket.
type auditSizeWriter struct {
	dst       io.Writer
	remaining int64
	message   string
}

func (w *auditSizeWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, errors.New(w.message)
	}
	n, err := w.dst.Write(data)
	w.remaining -= int64(n)
	return n, err
}

func (c *auditCSV) write(fields ...string) error {
	for i, field := range fields {
		fields[i] = excelExport.ProtectCSVText(field)
	}
	if err := c.out.Write(fields); err != nil {
		return err
	}
	c.rows++
	return nil
}

func (a *App) runAuditReport(job *ExportJob, label string, release func(), write func(context.Context, *auditCSV) error) {
	defer close(job.done)
	defer release()
	slot, err := acquireWorkSlot(job.ctx, a.exportSem)
	if err != nil {
		job.fail(err)
		return
	}
	defer slot()
	query, err := a.backgroundQuery(job.ctx)
	if err != nil {
		job.fail(err)
		return
	}
	defer query()
	job.mu.Lock()
	job.Status = "running"
	job.Stage = label
	job.mu.Unlock()
	var count int64
	err = publishExportFile(job, sourceExportName(job.wb.DisplayName, "selection", label, job.ID, "csv"), func(file *os.File) error {
		dst := &auditSizeWriter{file, formats.AuditReportBytes, "比较报告超过 1 GB，请缩小工作表或核查范围"}
		if _, err := io.WriteString(dst, "\ufeff"); err != nil {
			return err
		}
		report := &auditCSV{out: csv.NewWriter(dst)}
		if err := write(job.ctx, report); err != nil {
			return err
		}
		report.out.Flush()
		count = report.rows - 1
		return report.out.Error()
	})
	if err != nil {
		job.fail(err)
		return
	}
	job.mu.Lock()
	job.auditReport = true
	job.TotalRows = count
	job.mu.Unlock()
	job.complete(count)
}

func (a *App) handleOpenAuditReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeMessage(w, 400, "service.auditReport.reportParametersInvalid")
		return
	}
	a.mu.RLock()
	job := a.exports[req.ID]
	a.mu.RUnlock()
	if job == nil {
		writeMessage(w, 404, "service.auditReport.reportExpired")
		return
	}
	job.mu.RLock()
	ready := job.auditReport && job.Status == "done"
	path := job.FilePath
	job.mu.RUnlock()
	if !ready {
		writeMessage(w, 409, "service.auditReport.reportNotGeneratedYet")
		return
	}
	id, err := a.rootApp().OpenPathForLaunch(path)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	writeJSON(w, map[string]string{"url": "/?session=" + id})
}

type versionAlignment struct {
	Left, Right reconcileSourceRef
	Keys        []reconcile.Pair
}
type versionRequest struct {
	PrimaryBook, WorkbookKey, LeftBook, RightBook, LeftSheet, RightSheet string
	Alignment                                                            *versionAlignment
}
type versionPair struct{ left, right string }

func versionPairs(req versionRequest, left, right *workbook.Workbook) ([]versionPair, error) {
	if req.LeftSheet != "" || req.RightSheet != "" {
		l, okL := left.FindSheet(req.LeftSheet)
		r, okR := right.FindSheet(req.RightSheet)
		if !okL || !okR {
			return nil, i18n.Error("service.auditReport.selectSourceWorksheetOnBoth")
		}
		return []versionPair{{l.Name, r.Name}}, nil
	}
	available := make(map[string]bool, len(right.Sheets))
	for _, s := range right.Sheets {
		available[s.Name] = true
	}
	var pairs []versionPair
	for _, s := range left.Sheets {
		p := versionPair{left: s.Name}
		if available[s.Name] {
			p.right = s.Name
			delete(available, s.Name)
		}
		pairs = append(pairs, p)
	}
	for _, s := range right.Sheets {
		if available[s.Name] {
			pairs = append(pairs, versionPair{right: s.Name})
		}
	}
	return pairs, nil
}

func (a *App) handleVersionCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req versionRequest
	if decodeAnalysis(r, &req) != nil {
		writeMessage(w, 400, "service.auditReport.versionComparisonParametersInvalid")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	primary := a.currentWorkbook()
	if primary == nil || sourceBookID(primary) != req.PrimaryBook || primary.Fingerprint != req.WorkbookKey {
		writeMessage(w, 409, "service.auditReport.currentWorkbookChanged")
		return
	}
	left, right := a.resolveReconcileBook(req.LeftBook), a.resolveReconcileBook(req.RightBook)
	if left == nil || right == nil || left.SourceInfo.Incomplete || right.SourceInfo.Incomplete || left.ArchiveOrigin != nil && left.ArchiveOrigin.Partial || right.ArchiveOrigin != nil && right.ArchiveOrigin.Partial {
		writeMessage(w, 409, "service.auditReport.comparisonRequiresTwoAvailableComplete")
		return
	}
	if v := req.Alignment; v != nil {
		if req.LeftSheet == "" || req.RightSheet == "" || len(v.Keys) < 1 || len(v.Keys) > reconcile.MaxKeys ||
			v.Left.Book != req.LeftBook || v.Right.Book != req.RightBook || v.Left.Source != "wb:"+req.LeftSheet || v.Right.Source != "wb:"+req.RightSheet || v.Left.Stamp == "" || v.Right.Stamp == "" {
			writeMessage(w, 400, "service.auditReport.readFieldsTwoSelectedWorksheets")
			return
		}
	}
	pairs, err := versionPairs(req, left, right)
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	release := a.pinWorkbooks(left, right)
	job, err := a.newExportJob(primary, "csv")
	if err != nil {
		release()
		a.writeExportStartError(w, err)
		return
	}
	go a.runAuditReport(job, "版本比较", release, func(ctx context.Context, out *auditCSV) error {
		return a.writeVersionReport(ctx, job, out, [2]*workbook.Workbook{left, right}, pairs, req.Alignment)
	})
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func (a *App) versionSheet(ctx context.Context, book *workbook.Workbook, name string) (reconcile.VersionSheet, error) {
	s := reconcile.VersionSheet{Book: book}
	if name == "" {
		return s, nil
	}
	s.Meta, _ = book.FindSheet(name)
	s.Index = book.GetCachedIndex(s.Meta.Path)
	if s.Index != nil {
		return s, nil
	}
	release, err := a.backgroundIndex(ctx)
	if err != nil {
		return s, err
	}
	defer release()
	s.Index, err = book.BuildIndex(s.Meta, nil, ctx.Done())
	return s, err
}

func versionSide(cell *workbook.CellData) (typ, raw, formula, cache string) {
	if cell == nil {
		return
	}
	typ = cell.Type
	if cell.SourceType != "" {
		typ = cell.SourceType
		if cell.Currency != "" {
			typ += " " + cell.Currency
		}
	}
	if typ == "" {
		typ = "隐式"
	}
	cache = "常量"
	if cell.HasFormula {
		cache = "有缓存"
		if cell.CacheMissing {
			cache = "缺少缓存"
		}
		if cell.Formula == "" {
			cache += "（公式原文不可用）"
		}
	}
	return typ, cell.Raw, cell.Formula, cache
}

func (a *App) writeVersionReport(ctx context.Context, job *ExportJob, out *auditCSV, books [2]*workbook.Workbook, pairs []versionPair, alignment *versionAlignment) error {
	if err := out.write("变化", "基线工作表", "当期工作表", "原坐标", "基线类型", "基线原值", "基线公式", "基线状态", "当期类型", "当期原值", "当期公式", "当期状态", "基线说明", "当期说明", "基线源坐标", "当期源坐标", "业务主键(JSON)"); err != nil {
		return err
	}
	var hashes [2]string
	for i, book := range books {
		h, err := book.SourceFileSHA256(ctx)
		if err != nil {
			return err
		}
		hashes[i] = workbookDisplayName(book) + " SHA-256 " + h
	}
	meta := func(kind string, p versionPair, ref, l, r string) error {
		return out.write(kind, p.left, p.right, ref, "", "", "", "", "", "", "", "", l, r, "", "", "")
	}
	if err := meta("输入文件", versionPair{}, "", hashes[0], hashes[1]); err != nil {
		return err
	}
	first := out.rows
	if books[0].Date1904 != books[1].Date1904 {
		if err := meta("1904 日期系统", versionPair{}, "", strconv.FormatBool(books[0].Date1904), strconv.FormatBool(books[1].Date1904)); err != nil {
			return err
		}
	}
	ordinals := [2]map[string]int{{}, {}}
	for side, book := range books {
		for i, s := range book.Sheets {
			ordinals[side][s.Name] = i + 1
		}
	}
	for i, p := range pairs {
		l, err := a.versionSheet(ctx, books[0], p.left)
		if err != nil {
			return err
		}
		r, err := a.versionSheet(ctx, books[1], p.right)
		if err != nil {
			return err
		}
		if p.left == "" || p.right == "" || p.left != p.right {
			kind := "工作表映射"
			if p.left == "" {
				kind = "新增工作表"
			} else if p.right == "" {
				kind = "删除工作表"
			}
			if err := meta(kind, p, "", p.left, p.right); err != nil {
				return err
			}
		}
		shape := func(s reconcile.VersionSheet, pos int) string {
			if s.Index == nil {
				return ""
			}
			state := s.Meta.State
			if state == "" {
				state = "visible"
			}
			return fmt.Sprintf("表序 %d，%s，最大行 %d，最大列 %d，有记录行 %d", pos, state, s.Index.MaxRow, s.Index.MaxCol, s.Index.RowCount())
		}
		x, y := shape(l, ordinals[0][p.left]), shape(r, ordinals[1][p.right])
		if x != y {
			if err := meta("工作表结构", p, "", x, y); err != nil {
				return err
			}
		}
		if err := reconcile.CompareVersionLayout(ctx, l, r, func(d reconcile.VersionChange) error { return meta(d.Kind, p, d.Ref, d.LeftInfo, d.RightInfo) }); err != nil {
			return err
		}
		emit := func(d reconcile.VersionChange) error {
			a1, a2, a3, a4 := versionSide(d.Before)
			b1, b2, b3, b4 := versionSide(d.After)
			var leftDisplay, rightDisplay string
			if d.Before != nil {
				leftDisplay = d.Before.Display
			}
			if d.After != nil {
				rightDisplay = d.After.Display
			}
			leftRef, rightRef := d.LeftRef, d.RightRef
			if d.Before != nil {
				leftRef = d.Before.Ref
			} else {
				leftDisplay = d.LeftInfo
			}
			if d.After != nil {
				rightRef = d.After.Ref
			} else {
				rightDisplay = d.RightInfo
			}
			ref := d.Ref
			if alignment != nil && (leftRef != "" || rightRef != "") {
				ref = leftRef
				if ref == "" {
					ref = rightRef
				}
			}
			return out.write(d.Kind, p.left, p.right, ref, a1, a2, a3, a4, b1, b2, b3, b4, leftDisplay, rightDisplay, leftRef, rightRef, d.Key)
		}
		if alignment != nil {
			sheets := [2]reconcile.VersionSheet{l, r}
			var sources [2]reconcile.Source
			for side, ref := range [2]reconcileSourceRef{alignment.Left, alignment.Right} {
				cols := reconcile.NeededColumns(reconcile.Query{Keys: alignment.Keys}, side == 1)
				sources[side], err = a.buildReconcileSource(ctx, books[side], ref, cols)
				if err != nil {
					return err
				}
			}
			rule, _ := json.Marshal(alignment.Keys)
			if err := meta("业务主键对齐", p, string(rule), fmt.Sprintf("表头行 %d", alignment.Left.HeaderRow), fmt.Sprintf("表头行 %d", alignment.Right.HeaderRow)); err != nil {
				return err
			}
			beforeRows := out.rows
			err = reconcile.CompareKeyedVersionSheets(ctx, filepath.Join(job.wb.CacheDir, "queries"), sheets, sources, alignment.Keys, emit, func(stage string, done, total int64) {
				job.mu.Lock()
				job.Stage, job.DoneRows, job.TotalRows = stage, done, total
				job.mu.Unlock()
			})
			if err != nil {
				return err
			}
			if out.rows == beforeRows {
				if err := meta("业务行对齐后相同", p, "", "", ""); err != nil {
					return err
				}
			}
			continue
		}
		if err := reconcile.CompareVersionSheets(ctx, l, r, emit, func(n int) {
			job.mu.Lock()
			job.Stage = fmt.Sprintf("比较工作表 %d/%d · %s / %s", i+1, len(pairs), p.left, p.right)
			job.DoneRows = int64(n)
			job.mu.Unlock()
		}); err != nil {
			return err
		}
	}
	for _, book := range books {
		if err := book.CheckSourceUnchanged(); err != nil {
			return err
		}
	}
	if out.rows == first {
		return meta("所比较项目相同", versionPair{}, "", "", "")
	}
	return nil
}
