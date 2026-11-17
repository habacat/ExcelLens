package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func namedViewFixture(t *testing.T) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "views.csv")
	if err := os.WriteFile(file, []byte("项目,金额,编号\nitem10,9007199254740993.31,001\nitem2,2,002\nitem1,9007199254740993.30,003\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1)
	return a, file
}

func namedViewCatalog(t *testing.T, a *App) namedViewStore {
	t.Helper()
	return decodeResponse[struct{ Store namedViewStore }](t, analysisRequest(t, a, "GET", "/api/named-views", "")).Store
}

func viewConfigFixture() namedViewConfig {
	return namedViewConfig{Freeze: freezeSettings{Rows: 1, Cols: 1}, Layout: namedViewLayout{Rows: map[string]int{"2": 35}, Cols: map[string]int{"2": 220}, Font: "宋体", ColumnMode: "show-all", ColumnVisibility: map[string]bool{"3": true}}, Table: &namedViewTable{Row: 1, StartCol: 1, EndCol: 3}, Zoom: 110, ShowStyles: false, ShowFormulas: true}
}

func saveViewRequest(a *App, name, filterID string, revision int64) map[string]any {
	wb := a.currentWorkbook()
	return map[string]any{"action": "save", "name": name, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint, "sheet": wb.Sheets[0].Name, "filterId": filterID, "headerRow": 1, "expectedRevision": revision, "config": viewConfigFixture()}
}

func startViewFilter(t *testing.T, a *App, spec filterSpec) *FilterJob {
	t.Helper()
	wb := a.currentWorkbook()
	data := map[string]any{"sheet": wb.Sheets[0].Name, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "headerRow": spec.HeaderRow, "rules": spec.Rules, "sortKeys": spec.SortKeys, "sortColumn": spec.SortColumn, "descending": spec.Descending}
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/filter/start", data))
	a.mu.RLock()
	job := a.filterJobs[started.JobID]
	a.mu.RUnlock()
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("view filter timed out")
	}
	if job.Status != "done" {
		t.Fatal(job.Status, job.Error)
	}
	return job
}

func TestNamedViewCapturesCommittedQueryAndSurvivesCacheCleanupRenameAndRestart(t *testing.T) {
	a, file := namedViewFixture(t)
	wb := a.currentWorkbook()
	spec := filterSpec{HeaderRow: 1, Rules: []workbook.FilterRule{{Column: 2, Operator: "in", Values: []string{"9007199254740993.31", "9007199254740993.30"}}}, SortKeys: []workbook.SortKey{{Column: 1, Mode: "natural"}}}
	job := startViewFilter(t, a, spec)
	request := saveViewRequest(a, "核对常用", job.ResultID, 0)
	request["headerRow"] = 0 // editable controls do not replace committed rules
	saved := decodeResponse[struct {
		Store namedViewStore
		ID    string
	}](t, reconcilePost(t, a, "/api/named-views", request))
	view := saved.Store.Items[0]
	if saved.Store.Revision != 1 || !reflect.DeepEqual(view.Query, spec) || len(view.Source.Fields) != 2 || view.Source.Fields[0].RawName != "项目" || !reflect.DeepEqual(view.Config, viewConfigFixture()) {
		t.Fatal("saved view did not capture the actual query or complete configuration", view)
	}
	oldPath, _ := namedViewsPath(wb)
	copy := filepath.Join(t.TempDir(), "renamed.csv")
	data, _ := os.ReadFile(file)
	if err := os.WriteFile(copy, data, 0600); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, _ := New()
	defer b.Close()
	if _, err := b.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wb.CacheDir); !os.IsNotExist(err) {
		t.Fatal("probe did not remove disposable cache", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("cache maintenance removed saved views", err)
	}
	if err := b.OpenPath(copy); err != nil {
		t.Fatal(err)
	}
	store := namedViewCatalog(t, b)
	if store.Revision != 1 || store.Items[0].ID != view.ID || len(b.filterJobs) != 0 || len(b.filterResults) != 0 {
		t.Fatal("listing views either lost them or automatically ran a query")
	}
	current := b.currentWorkbook()
	resolved := decodeResponse[struct{ View namedView }](t, reconcilePost(t, b, "/api/named-views/resolve", map[string]string{"id": view.ID, "book": sourceBookID(current), "workbookKey": current.Fingerprint}))
	if !reflect.DeepEqual(resolved.View.Config, view.Config) || !reflect.DeepEqual(resolved.View.Query, spec) {
		t.Fatal("resolving a byte-equivalent renamed source changed the stored view")
	}
	replay := startViewFilter(t, b, resolved.View.Query)
	result := b.filterSnapshot(replay.ResultID)
	defer result.close()
	positions, _ := result.Rows.Page(0, result.Rows.Len())
	if !reflect.DeepEqual(positions, []int{0, 3, 1}) {
		t.Fatal("replayed exact filter/natural sort changed row order", positions)
	}
	after, _ := os.ReadFile(file)
	if !reflect.DeepEqual(after, data) {
		t.Fatal("view storage changed source bytes")
	}
}

func TestNamedViewRejectsStaleListsExpiredQueriesAndChangedSource(t *testing.T) {
	a, file := namedViewFixture(t)
	saved := decodeResponse[struct{ Store namedViewStore }](t, reconcilePost(t, a, "/api/named-views", saveViewRequest(a, "原视图", "", 0)))
	if out := reconcilePost(t, a, "/api/named-views", saveViewRequest(a, "第二个", "", 0)); out.Code != 409 {
		t.Fatal("stale catalog overwrote another page", out.Code, out.Body.String())
	}
	if out := reconcilePost(t, a, "/api/named-views", saveViewRequest(a, "已过期", "missing-result", 1)); out.Code != 409 {
		t.Fatal("expired filter silently became an unfiltered view", out.Code)
	}
	if out := reconcilePost(t, a, "/api/named-views", saveViewRequest(a, "原视图", "", 1)); out.Code != 409 {
		t.Fatal("duplicate name silently replaced a view", out.Code)
	}
	if err := os.WriteFile(file, []byte("changed,source\nnew,bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	out := reconcilePost(t, a, "/api/named-views/resolve", map[string]string{"id": saved.Store.Items[0].ID, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint})
	if out.Code != 409 || len(a.filterJobs) != 0 {
		t.Fatal("changed live source accepted or a query ran during validation", out.Code, out.Body.String())
	}
	if store := namedViewCatalog(t, a); store.Revision != 1 || len(store.Items) != 1 {
		t.Fatal("failed operations changed saved state", store)
	}
}

func TestNamedViewFieldAndRuleVersionsCannotSilentlyRebind(t *testing.T) {
	a, _ := namedViewFixture(t)
	job := startViewFilter(t, a, filterSpec{HeaderRow: 1, SortKeys: []workbook.SortKey{{Column: 1, Mode: "natural"}}})
	saved := decodeResponse[struct{ Store namedViewStore }](t, reconcilePost(t, a, "/api/named-views", saveViewRequest(a, "字段核对", job.ResultID, 0)))
	wb := a.currentWorkbook()
	path, _ := namedViewsPath(wb)
	for _, change := range []func(*namedView){
		func(v *namedView) { v.RuleVersion = 2 },
		func(v *namedView) { v.Source.Fields[0].HeaderHash = "other-header" },
		func(v *namedView) { v.Source.SheetPath = "other-sheet" },
		func(v *namedView) { v.Config.Layout.Cols = map[string]int{"0": 120} },
	} {
		encoded, _ := json.Marshal(saved.Store)
		var store namedViewStore
		_ = json.Unmarshal(encoded, &store)
		change(&store.Items[0])
		if err := writeSavedConfig(path, maxNamedViewsBytes, store, durableConfigOps); err != nil {
			t.Fatal(err)
		}
		out := reconcilePost(t, a, "/api/named-views/resolve", map[string]string{"id": store.Items[0].ID, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint})
		if out.Code != 409 {
			t.Fatal("unsupported or mismatched saved view accepted", out.Code, out.Body.String())
		}
	}
	if !strings.Contains(string(func() []byte { data, _ := os.ReadFile(path); return data }()), "字段核对") {
		t.Fatal("failed resolve discarded stored evidence")
	}
}

func TestNamedViewConcurrentPagesUseOneFileRevision(t *testing.T) {
	a, file := namedViewFixture(t)
	b, _ := New()
	defer b.Close()
	if err := b.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	requests := []map[string]any{saveViewRequest(a, "甲页", "", 0), saveViewRequest(b, "乙页", "", 0)}
	apps := []*App{a, b}
	codes := make([]int, 2)
	var group sync.WaitGroup
	for i := range apps {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			codes[i] = reconcilePost(t, apps[i], "/api/named-views", requests[i]).Code
		}(i)
	}
	group.Wait()
	if !((codes[0] == 200 && codes[1] == 409) || (codes[1] == 200 && codes[0] == 409)) {
		t.Fatal("two concurrent stale writers both committed", codes)
	}
	store := namedViewCatalog(t, a)
	if store.Revision != 1 || len(store.Items) != 1 {
		t.Fatal(store)
	}
}

func TestCancelledLateFilterReleasesPublishedResultButKeepsExistingReader(t *testing.T) {
	a, _ := namedViewFixture(t)
	job := startViewFilter(t, a, filterSpec{HeaderRow: 1, SortKeys: []workbook.SortKey{{Column: 1, Mode: "natural"}}})
	reader := a.filterSnapshot(job.ResultID)
	defer reader.close()
	reconcilePost(t, a, "/api/filter/cancel", map[string]string{"id": job.ID})
	if a.filterSnapshot(job.ResultID) != nil {
		t.Fatal("cancelled late filter result remained in the application result map")
	}
	status := decodeResponse[struct{ Status, Error string }](t, analysisRequest(t, a, "GET", "/api/filter/status?id="+job.ID, ""))
	if status.Status != "error" || !strings.Contains(status.Error, "过期或取消") {
		t.Fatal("released result advertised as a usable completed query", status)
	}
	if positions, err := reader.Rows.Page(0, 4); err != nil || !reflect.DeepEqual(positions, []int{0, 3, 2, 1}) {
		t.Fatal("late cancellation broke an independently retained reader", positions, err)
	}
}
