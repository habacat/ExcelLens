// archiveprobe is a local QA tool. It reports structure only, never cell values.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"time"

	"excellens/internal/archives"
	"excellens/internal/workbook"
)

func main() {
	path := flag.String("file", "", "local archive path")
	member := flag.Int("member", 0, "member to extract, or -1 to list only")
	index := flag.Bool("index", false, "index the first worksheet")
	flag.Parse()
	started := time.Now()
	a, err := archives.Open(context.Background(), *path, "")
	if err != nil {
		log.Fatal(err)
	}
	defer a.Close()
	result := map[string]any{"format": a.Format, "members": a.Members, "notice": a.Notice}
	if *member >= 0 {
		out, entry, err := a.Extract(context.Background(), *member, nil)
		if err != nil {
			log.Fatal(err)
		}
		result["extractedPath"] = out
		wb, err := workbook.OpenWorkbook(out)
		if err != nil {
			log.Fatal(err)
		}
		wb.SetDisplayName(entry.Name)
		result["sheetCount"], result["source"] = len(wb.Sheets), wb.SourceInfo
		result["sheets"] = wb.Sheets[:min(len(wb.Sheets), 8)]
		result["mergeCount"] = len(wb.HTMLMerges(wb.Sheets[0]))
		if *index {
			idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
			if err != nil {
				log.Fatal(err)
			}
			result["rows"], result["columns"] = idx.MaxRow, idx.MaxCol
		}
	}
	result["elapsedMs"] = time.Since(started).Milliseconds()
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		log.Fatal(err)
	}
}
