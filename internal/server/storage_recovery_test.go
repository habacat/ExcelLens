package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/localstore"
)

func TestRestrictedStartupReplaysWithoutTransferHistoryOrBackup(t *testing.T) {
	isolateTestStorage(t)
	roots, err := dataRoots()
	if err != nil {
		t.Fatal(err)
	}
	src := localstore.Roots{Config: t.TempDir(), Local: t.TempDir()}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src.Local, "exports", "result.csv"), "restored")
	backup, err := src.Backup(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(roots.Local, "exports", "result.csv")
	err = roots.RestoreWith(t.Context(), backup, localstore.TransferOptions{Report: func(p localstore.Progress) {
		if p.Phase == "提交恢复事务" && p.Files == 0 {
			write(target, "keep")
		}
	}})
	if err == nil || !roots.Pending() {
		t.Fatal("expected committed conflict", err)
	}
	if err := os.Rename(backup, backup+"-offline"); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(roots.Config, "transfer-history.json"), "broken")
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	page := sessionCall(t, app, "", "GET", "/", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "继续恢复") || strings.Contains(page.Body.String(), "fileInput") {
		t.Fatal("restricted page unavailable")
	}
	for _, path := range []string{"/api/status", "/api/background", "/api/interface", "/api/open", "/api/storage/sources"} {
		if res := sessionCall(t, app, "", "GET", path, ""); res.Code != 503 || res.Header().Get("X-ExcelLens-Recovery") != "required" {
			t.Fatal(path, res.Code)
		}
	}
	if res := sessionCall(t, app, "", "GET", "/api/storage", ""); res.Code != 200 {
		t.Fatal("inventory unavailable", res.Code)
	}
	if res := sessionCall(t, app, "", "POST", "/api/storage", `{"action":"clear-uploads"}`); res.Code != 503 {
		t.Fatal("writable inventory", res.Code)
	}
	if _, err := app.PreparePathForLaunch(target); err == nil {
		t.Fatal("native open allowed")
	}
	retry := func() {
		t.Helper()
		if res := sessionCall(t, app, "", "POST", "/api/storage/recovery", ""); res.Code != 200 {
			t.Fatal(res.Body.String())
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			r := sessionCall(t, app, "", "GET", "/api/storage/recovery", "")
			if strings.Contains(r.Body.String(), `"running":false`) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("replay did not settle")
	}
	retry()
	if got, _ := os.ReadFile(target); string(got) != "keep" || !roots.Pending() {
		t.Fatal("conflict overwritten")
	}
	if err := os.Rename(target, target+".saved"); err != nil {
		t.Fatal(err)
	}
	retry()
	if got, _ := os.ReadFile(target); string(got) != "restored" || roots.Pending() {
		t.Fatal("replay failed")
	}
	if res := sessionCall(t, app, "", "GET", "/api/status", ""); res.Code != 200 {
		t.Fatal(res.Code)
	}
	history := filepath.Join(roots.Config, "transfer-history.json")
	jobs := func() struct{ HistoryIssue *struct{ Revision string } } {
		return decodeResponse[struct{ HistoryIssue *struct{ Revision string } }](t, sessionCall(t, app, "", "GET", "/api/storage/jobs", ""))
	}
	issue := jobs().HistoryIssue
	if issue == nil {
		t.Fatal("missing repair action")
	}
	body, _ := json.Marshal(map[string]string{"action": "backup", "directory": t.TempDir()})
	waitStorageTransfer(t, app, string(body))
	if b, _ := os.ReadFile(history); string(b) != "broken" {
		t.Fatal("backup changed damaged history")
	}
	req := func(confirm bool, rev string) int {
		b, _ := json.Marshal(map[string]any{"action": "repair-history", "revision": rev, "confirm": confirm})
		return sessionCall(t, app, "", "POST", "/api/storage", string(b)).Code
	}
	if req(false, issue.Revision) != 400 {
		t.Fatal("repair without confirmation")
	}
	write(history, "new evidence")
	if req(true, issue.Revision) != 409 {
		t.Fatal("stale confirmation")
	}
	if req(true, jobs().HistoryIssue.Revision) != 200 || jobs().HistoryIssue != nil {
		t.Fatal("repair failed")
	}
	copies, _ := filepath.Glob(filepath.Join(roots.Config, "transfer-history-damaged-*.json"))
	if len(copies) != 1 {
		t.Fatal(copies)
	}
	if b, _ := os.ReadFile(copies[0]); string(b) != "new evidence" {
		t.Fatal("diagnostic mismatch")
	}
	waitStorageTransfer(t, app, string(body))

}

func TestListenerServesRecoveryWhileReplayBlocked(t *testing.T) {
	isolateTestStorage(t)
	roots, _ := dataRoots()
	os.MkdirAll(roots.Config, 0700)
	if err := os.WriteFile(filepath.Join(roots.Config, ".restore-transaction.json"), []byte("invalid journal"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.restoreState.startup {
		t.Fatal("constructor read the journal")
	}
	a.storageView.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			a.storageView.Unlock()
		}
	}()
	ln, url, err := a.Listen()
	if err != nil {
		t.Fatal(err)
	}
	go a.Serve(ln)
	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", url+"api/storage/recovery", nil)
	req.Header.Set("X-ExcelLens-Token", a.token)
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var state struct{ Running, Pending bool }
	if json.NewDecoder(r.Body).Decode(&state) != nil || !state.Running || !state.Pending {
		t.Fatal(state, r.Status)
	}
	a.storageView.Unlock()
	unlocked = true
}
