package server

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
)

const analysisDisplayRoute = "/api/settings/analysis-display"

func displayConfig(t *testing.T, a *App) analysisDisplayConfig {
	t.Helper()
	return decodeResponse[analysisDisplayConfig](t, analysisRequest(t, a, "GET", analysisDisplayRoute, ""))
}

func displayRequest(revision int64, group, scientific bool, exponent int) map[string]any {
	return map[string]any{"expectedRevision": revision, "options": analysisDisplayOptions{group, scientific, exponent}}
}

func TestAnalysisDisplayDefaultsPersistWithoutWorkbookAndAcrossAppRestart(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	got := displayConfig(t, a)
	if got.Version != 1 || got.Revision != 0 || got.Options != (analysisDisplayOptions{true, true, 12}) {
		t.Fatal(got)
	}
	path, _ := savedConfigPath("settings", "analysis-display")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("reading default preferences must not create a file", err)
	}
	saved := decodeResponse[analysisDisplayConfig](t, reconcilePost(t, a, analysisDisplayRoute, displayRequest(0, false, true, 9)))
	if saved.Revision != 1 || saved.Options != (analysisDisplayOptions{false, true, 9}) {
		t.Fatal(saved)
	}
	a.Close()
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if reopened := displayConfig(t, b); reopened != saved {
		t.Fatal("preferences were tied to the closed app, workbook or browser origin", reopened, saved)
	}
	updated := decodeResponse[analysisDisplayConfig](t, reconcilePost(t, b, analysisDisplayRoute, displayRequest(1, true, false, 15)))
	if updated.Options != (analysisDisplayOptions{true, false, 15}) {
		t.Fatal("settings are not independent", updated)
	}
}

func TestAnalysisDisplayConcurrentTabsRequireCurrentRevision(t *testing.T) {
	isolateTestStorage(t)
	const count = 8
	apps := make([]*App, count)
	for i := range apps {
		var err error
		apps[i], err = New()
		if err != nil {
			t.Fatal(err)
		}
		defer apps[i].Close()
	}
	codes := make(chan int, count)
	var group sync.WaitGroup
	for i, a := range apps {
		group.Add(1)
		go func() {
			defer group.Done()
			codes <- reconcilePost(t, a, analysisDisplayRoute, displayRequest(0, i%2 == 0, i%3 == 0, 9+3*(i%3))).Code
		}()
	}
	group.Wait()
	close(codes)
	successes := 0
	for code := range codes {
		if code == 200 {
			successes++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if successes != 1 || displayConfig(t, apps[0]).Revision != 1 {
		t.Fatal("stale pages overwrote a confirmed preference", successes)
	}
}

func TestAnalysisDisplayRejectsIncompleteInvalidAndDamagedSettingsWithoutReplacingFile(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	decodeResponse[analysisDisplayConfig](t, reconcilePost(t, a, analysisDisplayRoute, displayRequest(0, true, true, 12)))
	path, _ := savedConfigPath("settings", "analysis-display")
	before, _ := os.ReadFile(path)
	valid := `{"expectedRevision":1,"options":{"groupThousands":true,"scientific":true,"scientificExponent":12}}`
	for _, body := range []string{
		`{}`, `{"expectedRevision":1,"options":null}`,
		strings.Replace(valid, `"groupThousands":true,`, "", 1),
		strings.Replace(valid, `"scientific":true`, `"scientific":null`, 1),
		strings.Replace(valid, `"scientificExponent":12`, `"scientificExponent":308`, 1),
		strings.Replace(valid, `"expectedRevision":1`, `"expectedRevision":-1`, 1),
		strings.Replace(valid, `"options":`, `"unknown":false,"options":`, 1),
		valid + `{}`, strings.Repeat(" ", analysisDisplayBytes+1),
	} {
		if out := analysisRequest(t, a, "POST", analysisDisplayRoute, body); out.Code != 400 {
			t.Fatal(out.Code, out.Body.String())
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("invalid request replaced saved preferences")
		}
	}
	for _, value := range []any{
		map[string]any{"version": 1, "revision": 1, "options": map[string]any{"scientific": true, "scientificExponent": 12}},
		analysisDisplayConfig{Version: 2, Revision: 1, Options: analysisDisplayOptions{true, true, 12}},
		analysisDisplayConfig{Version: 1, Revision: maxConfigRevision, Options: analysisDisplayOptions{true, true, 12}},
	} {
		if err := writeSavedConfig(path, analysisDisplayBytes, value, durableConfigOps); err != nil {
			t.Fatal(err)
		}
		damaged, _ := os.ReadFile(path)
		if out := analysisRequest(t, a, "GET", analysisDisplayRoute, ""); out.Code != 422 {
			t.Fatal("invalid stored options silently became defaults", out.Code, out.Body.String())
		}
		if out := reconcilePost(t, a, analysisDisplayRoute, displayRequest(1, false, false, 9)); out.Code != 422 {
			t.Fatal(out.Code, out.Body.String())
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(damaged, after) {
			t.Fatal("damaged preference evidence was discarded")
		}
	}
	if out := analysisRequest(t, a, "DELETE", analysisDisplayRoute, ""); out.Code != 405 {
		t.Fatal(out.Code)
	}
}
