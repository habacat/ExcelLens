package workbook

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "excellens-workbook-tests-")
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

func TestStorageRootUsesPlatformTemporaryDirectory(t *testing.T) {
	root := isolateTestStorage(t)
	want := filepath.Join(root, "cache", "ExcelLens")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(root, "home", "Library", "Caches", "ExcelLens")
	}
	if got := DataRoot(); got != want {
		t.Fatalf("storage escaped test sandbox: got %q want %q", got, want)
	}
}
