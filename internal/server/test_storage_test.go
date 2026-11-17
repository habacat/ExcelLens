package server

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"excellens/internal/workbook"
)

// Set an isolated default for the entire test process, including tests which
// do not explicitly need persistent storage. Never write unit-test workbooks,
// recent-file history, cache indexes, or editable analyses into a real user's
// %LOCALAPPDATA% / %APPDATA% ExcelLens folders.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "excellens-server-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create isolated test root:", err)
		os.Exit(1)
	}
	configureTestStorage(root, os.Setenv)
	code := m.Run()
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, "cannot clean isolated test root:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// Each test that relies on empty persistent state gets its own directories,
// while opening and closing several App instances inside that test continues
// to exercise persistence across restarts.
func isolateTestStorage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	configureTestStorage(root, func(key, value string) error {
		t.Setenv(key, value)
		return nil
	})
	return root
}

func configureTestStorage(root string, set func(string, string) error) {
	// On Windows os.UserCacheDir uses %LocalAppData% and os.UserConfigDir
	// uses %AppData%. XDG_* alone (used by Linux) does not isolate Windows.
	cache, config := filepath.Join(root, "cache"), filepath.Join(root, "config")
	for _, item := range [][2]string{
		{"LOCALAPPDATA", cache}, {"APPDATA", config},
		{"XDG_CACHE_HOME", cache}, {"XDG_CONFIG_HOME", config},
		{"HOME", filepath.Join(root, "home")},
	} {
		if err := set(item[0], item[1]); err != nil {
			panic(fmt.Sprintf("cannot set test storage %s: %v", item[0], err))
		}
	}
}

// Regression test: local persistent files must be redirected on both Windows
// (%LOCALAPPDATA% / %APPDATA%) and Unix (XDG_CACHE_HOME/XDG_CONFIG_HOME).
func TestStorageIsolationUsesNativePlatformDirectories(t *testing.T) {
	root := isolateTestStorage(t)
	wantCache := filepath.Join(root, "cache", "ExcelLens")
	if runtime.GOOS == "darwin" {
		wantCache = filepath.Join(root, "home", "Library", "Caches", "ExcelLens")
	}
	if got := workbook.DataRoot(); got != wantCache {
		t.Fatalf("cache root escaped test sandbox: got %q, want %q", got, wantCache)
	}
	wb := &workbook.Workbook{Fingerprint: "test-only-fingerprint"}
	wantWorkspace := filepath.Join(root, "config", "ExcelLens", "workspaces", "test-only-fingerprint.json")
	if runtime.GOOS == "darwin" {
		wantWorkspace = filepath.Join(root, "home", "Library", "Application Support", "ExcelLens", "workspaces", "test-only-fingerprint.json")
	}
	if got := (&App{}).analysisPath(wb); got != wantWorkspace {
		t.Fatalf("workspace escaped test sandbox: got %q, want %q", got, wantWorkspace)
	}
	if got := recentPath(); got != filepath.Join(wantCache, "recent.json") {
		t.Fatalf("recent history escaped test sandbox: got %q", got)
	}
}
