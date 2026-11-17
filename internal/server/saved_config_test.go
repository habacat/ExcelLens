package server

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedConfigKeepsPreviousConfirmedFileWhenSyncOrReplacementFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := writeSavedConfig(path, 4096, map[string]string{"name": "原设置 <安全>"}, durableConfigOps); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	sentinel := errors.New("injected storage failure")
	for _, ops := range []configWriteOps{
		{sync: func(*os.File) error { return sentinel }, rename: os.Rename},
		{sync: (*os.File).Sync, rename: func(string, string) error { return sentinel }},
	} {
		if err := writeSavedConfig(path, 4096, map[string]string{"name": "新设置"}, ops); !errors.Is(err, sentinel) {
			t.Fatal(err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("failed persistence destroyed the previous confirmed file")
		}
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) != 1 {
			t.Fatal("failed persistence retained temporary files", entries)
		}
	}
	var decoded map[string]string
	if found, err := readSavedConfig(path, 4096, &decoded); !found || err != nil || decoded["name"] != "原设置 <安全>" {
		t.Fatal(found, decoded, err)
	}
	if err := writeSavedConfig(path, 128, map[string]string{"name": strings.Repeat("长", 100)}, durableConfigOps); err == nil {
		t.Fatal("size budget ignored")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("over-budget write damaged previous state")
	}
}

func TestSavedConfigRejectsCorruptionUnknownVersionAndOversizedReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := writeSavedConfig(path, 4096, map[string]string{"name": "known"}, durableConfigOps); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	for _, data := range [][]byte{
		bytes.Replace(before, []byte("known"), []byte("other"), 1),
		bytes.Replace(before, []byte(`"version":1`), []byte(`"version":2`), 1),
		append(append([]byte{}, before...), []byte(`{}`)...),
		[]byte(strings.Repeat(" ", 4097)),
	} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		var decoded map[string]string
		if _, err := readSavedConfig(path, 4096, &decoded); err == nil {
			t.Fatal("untrusted local configuration was silently accepted", string(data[:min(len(data), 80)]))
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(data, after) {
			t.Fatal("read discarded corruption evidence")
		}
	}
}
