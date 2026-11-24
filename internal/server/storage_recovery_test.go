package server

import (
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
}
