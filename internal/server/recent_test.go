package server

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecentRequestBodyCannotBlockAnotherTabsList(t *testing.T) {
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	body, writer := io.Pipe()
	postDone := make(chan struct{})
	go func() {
		defer close(postDone)
		defer body.Close()
		app.handleRecent(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/recent", body))
	}()
	defer func() { _ = writer.Close(); <-postDone }()
	// Write waits until the handler is reading. Leave the JSON body unfinished.
	if _, err := io.WriteString(writer, "{"); err != nil {
		t.Fatal(err)
	}
	list := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		app.handleRecent(response, httptest.NewRequest("GET", "/api/recent", nil))
		list <- response.Code
	}()
	select {
	case code := <-list:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an unfinished request held the shared recent history lock")
	}
}

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
