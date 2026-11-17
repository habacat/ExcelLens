package server

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type gatedBookmarkBody struct {
	io.Reader
	entered, release chan struct{}
	started          bool
}

func (b *gatedBookmarkBody) Read(p []byte) (int, error) {
	if !b.started {
		b.started = true
		close(b.entered)
		<-b.release
	}
	return b.Reader.Read(p)
}
func (b *gatedBookmarkBody) Close() error { return nil }

func TestBookmarksSerializeWritesAcrossBrowserApps(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "shared.xlsx")
	writeMultiSheetWorkbook(t, path)
	a, b := prepareFeatureApp(t, path), prepareFeatureApp(t, path)
	defer a.Close()
	defer b.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	first := &gatedBookmarkBody{Reader: strings.NewReader(`{"action":"save","sheet":"First","ref":"A1","note":"first tab"}`), entered: entered, release: release}
	call := func(app *App, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/bookmarks", body)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-ExcelLens-Token", app.token)
		out := httptest.NewRecorder()
		app.Handler().ServeHTTP(out, req)
		return out
	}
	one, two := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { one <- call(a, first) }()
	<-entered // first transaction has loaded its snapshot and owns the file lock
	go func() {
		two <- call(b, strings.NewReader(`{"action":"save","sheet":"First","ref":"B1","note":"second tab"}`))
	}()
	select {
	case <-two:
		close(release)
		<-one
		t.Fatal("second tab bypassed the lock and can overwrite the first snapshot")
	case <-time.After(40 * time.Millisecond):
	}
	close(release)
	for _, ch := range []chan *httptest.ResponseRecorder{one, two} {
		select {
		case out := <-ch:
			if out.Code != 200 {
				t.Fatal(out.Code, out.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("bookmark transaction did not finish")
		}
	}
	var store bookmarkStore
	if err := json.Unmarshal(featureCall(t, a, "GET", "/api/bookmarks", "").Body.Bytes(), &store); err != nil || len(store.Items) != 2 {
		t.Fatal("a tab's mark was lost", store, err)
	}
}
