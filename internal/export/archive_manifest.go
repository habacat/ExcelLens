package export

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func csvArchiveNames(selections []Selection) []string {
	used := make(map[string]bool, len(selections))
	names := make([]string, 0, len(selections))
	for _, s := range selections {
		base := safePartName(s.Sheet.Name)
		name := base
		for suffix := 2; used[strings.ToLower(name)]; suffix++ {
			name = fmt.Sprintf("%s-%d", base, suffix)
		}
		used[strings.ToLower(name)] = true
		names = append(names, name+".csv")
	}
	return names
}

func csvArchiveManifest(sourceName, fingerprint string, selections []Selection, opts Options, names []string, created time.Time) ([]byte, error) {
	items := make([]map[string]any, 0, len(selections))
	for i, s := range selections {
		items = append(items, map[string]any{"sheet": s.Sheet.Name, "file": names[i], "rows": s.RowCount(), "firstColumn": s.FirstCol, "lastColumn": s.LastCol, "filtered": s.Filtered})
	}
	return json.MarshalIndent(map[string]any{"source": sourceName, "fingerprint": fingerprint, "createdAt": created.Format(time.RFC3339), "mode": opts.Mode, "sheets": items}, "", "  ")
}
