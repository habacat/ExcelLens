package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"excellens/internal/localstore"
)

// Pause at the actual localstore publication hook, on either side of the same
// lock used by HTTP cancellation. No timing or displayed progress is assumed.
func TestStorageCancellationAtPublication(t *testing.T) {
	for _, action := range []string{"backup", "restore"} {
		for _, cancelFirst := range []bool{true, false} {
			label := action + "/commit-wins"
			if cancelFirst {
				label = action + "/cancel-wins"
			}
			t.Run(label, func(t *testing.T) {
				isolateTestStorage(t)
				app, err := New()
				if err != nil {
					t.Fatal(err)
				}
				defer app.Close()
				roots, _ := dataRoots()
				src := localstore.Roots{Config: t.TempDir(), Local: t.TempDir()}
				if err := os.MkdirAll(filepath.Join(src.Local, "exports"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(src.Local, "exports", "x.csv"), []byte("123"), 0600); err != nil {
					t.Fatal(err)
				}
				backup, err := src.Backup(t.Context(), t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				parent := t.TempDir()
				atGate, proceed := make(chan struct{}), make(chan struct{})
				app.storageMu.Lock()
				job, err := app.submitStorage(roots, action, func(ctx context.Context, options localstore.TransferOptions) (transferResult, error) {
					claim := options.BeginCommit
					options.BeginCommit = func() error {
						if cancelFirst {
							close(atGate)
							<-proceed
							return claim()
						}
						e := claim()
						close(atGate)
						<-proceed
						return e
					}
					var e error
					if action == "backup" {
						_, e = src.BackupWith(ctx, parent, options)
					} else {
						e = roots.RestoreWith(ctx, backup, options)
					}
					return transferResult{}, e
				})
				if err != nil {
					app.storageMu.Unlock()
					t.Fatal(err)
				}
				select {
				case <-atGate:
				case <-time.After(10 * time.Second):
					close(proceed)
					t.Fatal("publication not reached")
				}
				response := sessionCall(t, app, "", "POST", "/api/storage/jobs?id="+job.ID, "{}")
				close(proceed)
				app.storageMu.Lock()
				app.storageMu.Unlock()
				final := decodeResponse[storageJob](t, featureCall(t, app, "GET", "/api/storage/jobs?id="+job.ID, ""))
				published := false
				if action == "backup" {
					entries, e := os.ReadDir(parent)
					if e != nil {
						t.Fatal(e)
					}
					published = len(entries) > 0
				} else {
					_, e := os.Stat(filepath.Join(roots.Local, "exports", "x.csv"))
					published = e == nil
				}
				if cancelFirst && (response.Code != 200 || published || final.State != "cancelled" || roots.Pending()) {
					t.Fatal(response.Code, published, final)
				}
				if !cancelFirst && (response.Code != 409 || !published || final.State != "done") {
					t.Fatal(response.Code, published, final)
				}
			})
		}
	}
}

func TestInventoryReadQueuesRestoreWithoutRejectingIt(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	roots, _ := dataRoots()
	folder, err := roots.Backup(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A reader which began before the click must not occupy writer admission.
	a.storageView.RLock()
	body, err := json.Marshal(map[string]string{"action": "restore", "directory": folder})
	if err != nil {
		a.storageView.RUnlock()
		t.Fatal(err)
	}
	response := sessionCall(t, a, "", "POST", "/api/storage", string(body))
	a.storageView.RUnlock()
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body)
	}
	var job storageJob
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	waitForStorage(t, a, job)
}
