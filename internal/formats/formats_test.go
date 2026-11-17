package formats

import (
	"strings"
	"testing"
)

func TestEntryPointsAgreeOnExtensions(t *testing.T) {
	seen := make(map[string]bool)
	for _, ext := range Extensions() {
		if seen[ext] || !Supported(strings.ToUpper(ext)) || DataExtension(ext) == ArchiveExtension(ext) {
			t.Fatalf("ambiguous or unsupported extension %q", ext)
		}
		seen[ext] = true
	}
	for _, ext := range []string{".xlsb", ".exe", "xlsx", "", ".html"} {
		if Supported(ext) {
			t.Fatalf("unexpected support for %q", ext)
		}
	}
	parts := strings.Split(DialogFilter(), "\x00")
	if len(parts) != 6 || parts[4] != "" || parts[5] != "" {
		t.Fatal("Windows filter is not paired and double-NUL terminated")
	}
	if parts[1] != "*"+strings.ReplaceAll(Accept(), ",", ";*") {
		t.Fatal("native and browser file filters differ")
	}
	copy := Extensions()
	copy[0] = ".xlsb"
	if Supported(copy[0]) {
		t.Fatal("caller changed the registry")
	}
}

func TestExternalBillionRowCapacityKeepsNativeExcelCeilings(t *testing.T) {
	for _, id := range []string{"csv", "tsv", "txt", "sqlite", "xls-html"} {
		source, ok := Lookup(id)
		if !ok || source.MaxRows != 1_000_000_000 {
			t.Fatal(id, source)
		}
	}
	for id, want := range map[string]int{"xlsx": 1_048_576, "xlsm": 1_048_576, "xls": 65_536} {
		source, _ := Lookup(id)
		if source.MaxRows != want {
			t.Fatal(id, source.MaxRows)
		}
	}
}
