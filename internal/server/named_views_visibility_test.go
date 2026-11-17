package server

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"excellens/internal/workbook"
)

func TestNamedViewCapturesSuccessfulVisibilityAndFullQueryAfterFilterOwnerClear(t *testing.T) {
	a, file := namedViewFixture(t)
	wb := a.currentWorkbook()
	sheet := wb.Sheets[0].Name
	query := filterSpec{HeaderRow: 1, Rules: []workbook.FilterRule{}, SortKeys: []workbook.SortKey{{Column: 1, Mode: "natural"}}}
	filtered := startViewFilter(t, a, query)
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: sheet, FilterID: filtered.ResultID, Change: &rowViewChange{Action: "range", First: 3, Last: 3, Hidden: true}}), "done")
	featureCall(t, a, "POST", "/api/filter/clear", `{"id":"`+filtered.ResultID+`"}`)
	request := saveViewRequest(a, "含行显隐", filtered.ResultID, 0)
	request["viewId"] = id
	request["headerRow"] = 0
	config := viewConfigFixture()
	config.RowVisibility = &workbook.AxisViewSettings{Mode: "hide-all"}
	request["config"] = config
	saved := decodeResponse[struct {
		Store namedViewStore
		ID    string
	}](t, reconcilePost(t, a, "/api/named-views", request))
	view := saved.Store.Items[0]
	if view.RuleVersion != 2 || !reflect.DeepEqual(view.Query, query) || view.Config.RowVisibility == nil || view.Config.RowVisibility.Mode != "original" || !reflect.DeepEqual(view.Config.RowVisibility.Ranges, []workbook.AxisVisibilityOverride{{Min: 3, Max: 3, Hidden: true}}) {
		t.Fatal("named view captured unexecuted client controls instead of its snapshot", view)
	}
	path, _ := namedViewsPath(wb)
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(id)) {
		t.Fatal("ephemeral visibility task/index identity leaked into saved view")
	}
	featureCall(t, a, "POST", "/api/row-view/clear", `{"id":"`+id+`"}`)
	copyPath := filepath.Join(t.TempDir(), "renamed.csv")
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, source, 0600); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.OpenPath(copyPath); err != nil {
		t.Fatal(err)
	}
	current := b.currentWorkbook()
	resolved := decodeResponse[struct{ View namedView }](t, reconcilePost(t, b, "/api/named-views/resolve", map[string]string{"id": view.ID, "book": sourceBookID(current), "workbookKey": current.Fingerprint}))
	if !reflect.DeepEqual(resolved.View.Config.RowVisibility, view.Config.RowVisibility) || len(b.rowViews) != 0 {
		t.Fatal("resolve changed visibility or automatically published a live view", resolved)
	}
	replay := startViewFilter(t, b, resolved.View.Query)
	newID := waitRowView(t, b, rowViewStart(t, b, rowViewRequest{Sheet: current.Sheets[0].Name, FilterID: replay.ResultID, Settings: resolved.View.Config.RowVisibility}), "done")
	if b.rowViews[newID].count() != 3 || b.filterResults[replay.ResultID].Rows.Len() != 4 {
		t.Fatal("replaying visibility changed complete matches", b.rowViews[newID].count(), b.filterResults[replay.ResultID].Rows.Len())
	}
	if after, err := os.ReadFile(file); err != nil || !bytes.Equal(after, source) {
		t.Fatal("named view changed source file", err)
	}
}

func TestNamedVisibilityRequiresBoundSnapshotAndRejectsInvalidSavedOutline(t *testing.T) {
	a, _ := namedViewFixture(t)
	wb := a.currentWorkbook()
	request := saveViewRequest(a, "缺少快照", "", 0)
	config := viewConfigFixture()
	config.RowVisibility = &workbook.AxisViewSettings{Mode: "show-all"}
	request["config"] = config
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := rowViewCall(t, a, "POST", "/api/named-views", string(encoded))
	if response.Code == 200 {
		t.Fatal("forged visibility configuration saved without a completed view")
	}
	id := waitRowView(t, a, rowViewStart(t, a, rowViewRequest{Sheet: wb.Sheets[0].Name}), "done")
	request = saveViewRequest(a, "有效快照", "", 0)
	request["viewId"] = id
	saved := decodeResponse[struct {
		Store namedViewStore
		ID    string
	}](t, reconcilePost(t, a, "/api/named-views", request))
	store := saved.Store
	settings := *store.Items[0].Config.RowVisibility
	settings.Groups = []workbook.AxisOutlineOverride{{AxisOutlineKey: workbook.AxisOutlineKey{Min: 90, Max: 100, Level: 1}, Collapsed: true}}
	store.Items[0].Config.RowVisibility = &settings
	path, _ := namedViewsPath(wb)
	if err := writeSavedConfig(path, maxNamedViewsBytes, store, durableConfigOps); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(map[string]string{"id": saved.ID, "book": sourceBookID(wb), "workbookKey": wb.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	response = rowViewCall(t, a, "POST", "/api/named-views/resolve", string(encoded))
	if response.Code != 409 {
		t.Fatal("saved nonexistent outline silently applied", response.Code, response.Body.String())
	}
	if a.rowViews[id].count() != 4 {
		t.Fatal("failed resolution replaced existing visible state")
	}
}
