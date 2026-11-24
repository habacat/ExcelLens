package transferjobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/localstore"
)

func TestUnavailableHistoryKeepsWorkingTransfersAndPreservesFile(t *testing.T) {
	for _, tc := range []struct{ name, history string }{
		{"newer format", `{"version":2,"extra":"preserve"}`},
		{"directory in place of file", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "transfer-history.json")
			var err error
			if tc.history == "" {
				err = os.Mkdir(file, 0700)
			} else {
				err = os.WriteFile(file, []byte(tc.history), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			var manager Manager
			done := make(chan struct{})
			_, err = manager.Start(file, "job-1", "backup", func(context.Context, localstore.TransferOptions) (Result, error) {
				return Result{Message: "completed"}, nil
			}, func() { close(done) })
			if err != nil {
				t.Fatal(err)
			}
			<-done
			jobs, issue := manager.List(file)
			if len(jobs) != 1 || jobs[0].State != "done" || issue == nil || issue.Reason == "" || issue.Revision != "" {
				t.Fatal(jobs, issue)
			}
			if _, err = manager.Repair(file, "unconfirmed"); err == nil {
				t.Fatal("offered to replace unreadable history")
			}
			if tc.history != "" {
				b, err := os.ReadFile(file)
				if err != nil || string(b) != tc.history {
					t.Fatal("future version was modified", err)
				}
			} else if info, err := os.Stat(file); err != nil || !info.IsDir() {
				t.Fatal("unexpected replacement", err)
			}
		})
	}
}
