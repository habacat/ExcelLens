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
	for _, ext := range []string{".exe", "xlsx", "", ".html"} {
		if Supported(ext) {
			t.Fatalf("unexpected support for %q", ext)
		}
	}
	parts := strings.Split(DialogFilter("Supported data files", "All files"), "\x00")
	if len(parts) != 6 || parts[4] != "" || parts[5] != "" {
		t.Fatal("Windows filter is not paired and double-NUL terminated")
	}
	if parts[1] != "*"+strings.ReplaceAll(Accept(), ",", ";*") {
		t.Fatal("native and browser file filters differ")
	}
	copy := Extensions()
	copy[0] = ".xlsx-test"
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
	for id, want := range map[string]int{"xlsx": ExcelRows, "xlsm": ExcelRows, "xlsb": ExcelRows, "xls": BIFFRows} {
		source, _ := Lookup(id)
		if source.MaxRows != want {
			t.Fatal(id, source.MaxRows)
		}
	}
}

func TestFormatForExtensionResolvesRegistry(t *testing.T) {
	want := map[string]string{
		".xlsx": "xlsx", ".XLSM": "xlsx", ".xlsb": "xlsb", ".xls": "xls",
		".ods": "ods", ".csv": "csv", ".tsv": "tsv", ".txt": "txt",
		".db": "sqlite", ".sqlite": "sqlite", ".SQLITE3": "sqlite",
	}
	for ext, expected := range want {
		got, ok := FormatForExtension(ext)
		if !ok || got != expected {
			t.Fatalf("FormatForExtension(%q) = (%q, %v), want (%q, true)", ext, got, ok, expected)
		}
	}
	for _, ext := range []string{".zip", ".rar", ".tar", ".exe", ""} {
		if got, ok := FormatForExtension(ext); ok {
			t.Fatalf("FormatForExtension(%q) unexpectedly returned %q", ext, got)
		}
	}
}
