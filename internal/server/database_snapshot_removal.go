package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/localstore"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

// Route before the normal per-request read lock. Check again on confirmation
// while excluding opens, new work registrations and storage transfers.
func (a *App) handleSnapshotRemoval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Key     string `json:"key"`
		Confirm bool   `json:"confirm"`
	}
	if !decodeDatabaseRequest(w, r, &req) {
		return
	}
	if workbook.DatabaseSnapshotPath(req.Key) == "" {
		writeError(w, 400, "数据库快照标识无效")
		return
	}
	if !a.storageMu.TryLock() {
		writeError(w, 409, "另一项数据管理操作正在进行")
		return
	}
	defer a.storageMu.Unlock()
	a.storageView.Lock()
	defer a.storageView.Unlock()
	a.cacheResetMu.Lock()
	defer a.cacheResetMu.Unlock()
	if a.restoreState.pending() {
		writeError(w, 409, "请先完成恢复事务")
		return
	}
	a.sessionGate.Lock()
	defer a.sessionGate.Unlock()
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	refs, err := a.snapshotReferences(r.Context(), req.Key)
	if err != nil {
		writeError(w, 409, "无法完整核对引用，已保留快照："+err.Error())
		return
	}
	if req.Confirm {
		if len(refs) != 0 {
			writeError(w, 409, "快照仍被引用："+strings.Join(refs, "、"))
			return
		}
		if err := workbook.RemoveDatabaseSnapshot(r.Context(), req.Key); err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	writeJSON(w, map[string]any{"references": refs, "removed": req.Confirm})
}

func (a *App) snapshotReferences(ctx context.Context, key string) ([]string, error) {
	roots, err := dataRoots()
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, wb := range a.referencedWorkbooks() {
		if wb.Fingerprint == key {
			refs = append(refs, "打开的工作簿、任务或对账结果")
		}
	}
	for _, viewer := range a.activeBrowserWorkbooks() {
		viewer.mu.RLock()
		pending := viewer.launchPath
		viewer.mu.RUnlock()
		if pending != "" && snapshotSourcePath(pending, key) {
			refs = append(refs, "待打开的工作簿")
		}
	}
	for _, group := range []struct {
		name  string
		paths func() ([]string, error)
	}{
		{"审计任务", savedTaskSources}, {"编辑底稿", editBasePaths},
	} {
		paths, err := group.paths()
		if err != nil {
			return nil, fmt.Errorf("%s：%w", group.name, err)
		}
		for _, path := range paths {
			if snapshotSourcePath(path, key) {
				refs = append(refs, group.name)
				break
			}
		}
	}
	recipes, err := readRecipes(filepath.Join(roots.Config, "recipes", "catalog.json"))
	if err != nil {
		return nil, err
	}
	for _, item := range recipes.Items {
		if item.Origin.Fingerprint == key {
			refs = append(refs, "分析方案「"+item.Name+"」")
		}
	}
	compositions, err := readCompositionDefinitions(filepath.Join(roots.Config, "compositions", "catalog.json"))
	if err != nil {
		return nil, err
	}
	for _, item := range compositions.Items {
		if usesSnapshot(&item.Definition, key) {
			refs = append(refs, "联合来源「"+item.Definition.Name+"」")
		}
	}
	results, err := readComparisonSnapshots(filepath.Join(roots.Config, "snapshots", "comparisons.json"))
	if err != nil {
		return nil, err
	}
	for _, item := range results.Items {
		if item.WorkbookKey == key || queryUsesSnapshot(item.Query, key) {
			refs = append(refs, "比较快照「"+item.Name+"」")
		}
	}
	work, err := snapshotWorkspaceReferences(ctx, roots, key)
	if err != nil {
		return nil, err
	}
	refs = append(refs, work...)
	settings, err := snapshotSavedReferences(roots, key)
	if err != nil {
		return nil, err
	}
	refs = append(refs, settings...)
	exports, err := snapshotExportReferences(ctx, key)
	if err != nil {
		return nil, err
	}
	refs = append(refs, exports...)
	drafts, err := a.snapshotDraftReferences(ctx, key)
	if err != nil {
		return nil, err
	}
	refs = append(refs, drafts...)
	slices.Sort(refs)
	return slices.Compact(refs), ctx.Err()
}

func snapshotSourcePath(path, key string) bool {
	return strings.EqualFold(filepath.Base(path), filepath.Base(workbook.DatabaseSnapshotPath(key))) &&
		strings.EqualFold(filepath.Base(filepath.Dir(path)), key)
}

func usesSnapshot(def *sourceview.Append, key string) bool {
	if def == nil {
		return false
	}
	for _, input := range def.Inputs {
		if input.Ref.Fingerprint == key {
			return true
		}
	}
	return false
}

func queryUsesSnapshot(q analysis.Query, key string) bool {
	return q.SourceScope != nil && q.SourceScope.Fingerprint == key || usesSnapshot(q.Composition, key)
}

func snapshotWorkspaceReferences(ctx context.Context, roots localstore.Roots, key string) ([]string, error) {
	var refs []string
	dir := filepath.Join(roots.Config, "workspaces")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// LoadWorkspace resolves a missing primary to its recovery checkpoint.
		name := strings.TrimSuffix(entry.Name(), ".bak")
		if seen[name] || !strings.HasSuffix(name, ".json") {
			continue
		}
		seen[name] = true
		w, err := analysis.LoadWorkspace(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("分析工作区 %s：%w", name, err)
		}
		own := name == key+".json"
		if own && len(w.Sheets) != 0 {
			refs = append(refs, "分析工作表")
		}
		for _, report := range w.Reports {
			if own || queryUsesSnapshot(report.Query, key) {
				refs = append(refs, "分析报告「"+report.Name+"」")
			}
		}
	}
	return refs, nil
}
