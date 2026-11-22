package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenProgramDirectoryUsesExecutableEvenFromAnotherFolder(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	failure := errors.New("explorer is unavailable")
	err = openProgramDirectory(func(dir string) error {
		if dir != filepath.Dir(exe) {
			t.Fatalf("Explorer target %q, executable %q", dir, exe)
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
