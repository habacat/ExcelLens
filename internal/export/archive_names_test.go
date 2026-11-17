package export

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func TestCSVArchiveNamesAreUniqueAndWindowsExtractable(t *testing.T) {
	var selections []Selection
	// The third name used to get an already-used generated suffix. Sources
	// such as HTML and SQLite may also supply case variants or reserved names.
	names := []string{"A-3", "A", "A ", "a", "CON", "aux.txt", "LPT1", "A-2"}
	for _, name := range names {
		selections = append(selections, Selection{Sheet: workbook.SheetMeta{Name: name}, Index: &workbook.SheetIndex{}, FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 1})
	}
	// An empty index still needs an existing source descriptor for the stream.
	root := exportTestStorage(t)
	path := root + "/empty.xml"
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for i := range selections {
		selections[i].Index.XMLPath = path
	}
	var out bytes.Buffer
	if err := Write(context.Background(), &out, &workbook.Workbook{}, selections, Options{Format: "zipcsv", Mode: "values"}, nil); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, file := range z.File {
		key := strings.ToLower(file.Name)
		if seen[key] {
			t.Fatalf("archive collision: %q", file.Name)
		}
		seen[key] = true
	}
	f, err := z.Open("_ExcelLens_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	var manifest struct {
		Sheets []struct{ Sheet, File string }
	}
	if err := json.Unmarshal(data, &manifest); err != nil || len(manifest.Sheets) != len(names) {
		t.Fatal("incomplete manifest", string(data), err)
	}
	for i, item := range manifest.Sheets {
		if item.Sheet != names[i] || !seen[strings.ToLower(item.File)] {
			t.Fatal("manifest lost the original sheet name", item)
		}
	}
	for _, unsafe := range []string{"con.csv", "aux.txt.csv", "lpt1.csv"} {
		if seen[unsafe] {
			t.Fatal("Windows reserved filename", unsafe)
		}
	}
}
