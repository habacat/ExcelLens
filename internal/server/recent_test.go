package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecentFilesReopenPinRemoveAndMissing(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "audited.xlsx")
	writeMultiSheetWorkbook(t, file)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	get := func() []recentEntry {
		resp := featureCall(t, a, "GET", "/api/recent", "")
		var val struct {
			Items []recentEntry `json:"items"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &val); err != nil {
			t.Fatal(err)
		}
		return val.Items
	}
	list := get()
	if len(list) != 1 || list[0].Name != "audited.xlsx" || !list[0].Available {
		t.Fatalf("list %+v", list)
	}
	id := list[0].ID
	featureCall(t, a, "POST", "/api/recent", `{"action":"pin","id":"`+id+`"}`)
	if !get()[0].Pinned {
		t.Fatal("pin not persisted")
	}
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	response := featureCall(t, b, "POST", "/api/recent", `{"action":"open","id":"`+id+`"}`)
	if !strings.Contains(response.Body.String(), "audited.xlsx") || b.currentWorkbook() == nil {
		t.Fatal("reopen failed")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	list = get()
	if len(list) != 1 || list[0].Available {
		t.Fatalf("missing not recognized %+v", list)
	}
	featureCall(t, a, "POST", "/api/recent", `{"action":"remove","id":"`+id+`"}`)
	if len(get()) != 0 {
		t.Fatal("remove record failed")
	}
}
