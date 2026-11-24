package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"excellens/internal/searchresults"
)

func TestCacheClearKeepsFullResultPublishedWhileDraining(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "ledger.csv")
	if err := os.WriteFile(path, []byte("id\n001\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := checkpointOpen(t, path)
	defer a.Close()
	wb := a.currentWorkbook()
	ctx, cancel := context.WithCancel(t.Context())
	job := &SearchJob{ID: "query", Full: true, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.searchJobs[job.ID] = job
	go func() {
		defer close(job.done)
		<-ctx.Done() // the clear has passed its first in-use check
		results, err := wb.NewSearchResults(t.Context())
		if err == nil {
			err = results.Append(searchresults.Hit{Sheet: wb.Sheets[0].Name, Row: 2, Col: 1, Ref: "A2", Value: "001"})
		}
		if err == nil {
			err = results.Seal()
		}
		if err != nil {
			results.Close()
			t.Error(err)
			return
		}
		job.mu.Lock()
		job.Results, job.Status = results, "done"
		job.mu.Unlock()
	}()
	if got := sessionCall(t, a, "", "POST", "/api/cache/clear", "{}"); got.Code != 409 {
		t.Fatalf("expected in-use conflict, got %d: %s", got.Code, got.Body)
	}
	page := decodeResponse[searchresults.Page](t, featureCall(t, a, "GET", "/api/search/page?id=query", ""))
	if len(page.Matches) != 1 || page.Matches[0].Value != "001" {
		t.Fatal(page)
	}
}

func TestFullSearchPagesExportAndRecoveredResults(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "ledger.csv")
	var csvData strings.Builder
	csvData.WriteString("id,value\n")
	long := "=office-" + strings.Repeat("x", 1200)
	for i := 0; i < 1307; i++ {
		value := "office"
		if i == 6 {
			value = long
		}
		fmt.Fprintf(&csvData, "%08d,%s\n", i, value)
	}
	if err := os.WriteFile(path, []byte(csvData.String()), 0600); err != nil {
		t.Fatal(err)
	}
	a := checkpointOpen(t, path)
	defer a.Close()
	wb := a.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, durable := range []bool{false, true} {
		body := searchRequest{Sheet: wb.Sheets[0].Name, Query: "office", Mode: "value", Scope: "all", Full: true, Recoverable: durable, WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb)}
		start := func(req searchRequest) string {
			t.Helper()
			resp := checkpointCall(t, a, "/api/search/start", req, 200)
			var x struct{ JobID string }
			json.Unmarshal(resp.Body.Bytes(), &x)
			return x.JobID
		}
		id := start(body)
		j := a.searchJobs[id]
		checkpointWait(t, j)
		if j.Status != "done" || j.HitCount != 1307 || len(j.Matches) != 0 {
			t.Fatal(j.Status, j.Error, j.HitCount)
		}
		if durable {
			m := checkpointManifest(t, j.TaskID)
			j = checkpointResume(t, a, m)
			checkpointWait(t, j)
			id = j.ID
			if j.Status != "done" || j.HitCount != 1307 || checkpointManifest(t, j.TaskID).Revision != m.Revision {
				t.Fatal(j.Status, j.Error)
			}
		}
		var after int64
		count := 0
		clipped := false
		for {
			page := decodeResponse[searchresults.Page](t, featureCall(t, a, "GET", fmt.Sprintf("/api/search/page?id=%s&after=%d", id, after), ""))
			clipped = clipped || page.PreviewClipped
			for _, hit := range page.Matches {
				count++
				if hit.Ref != "B"+strconv.Itoa(count+1) {
					t.Fatal(hit.Ref, count)
				}
			}
			if !page.More {
				break
			}
			if page.Next <= after {
				t.Fatal(page)
			}
			after = page.Next
		}
		if count != 1307 || !clipped {
			t.Fatal(count, clipped)
		}
		filter := url.Values{"id": {id}, "text": {"=office"}, "sheet": {wb.Sheets[0].Name}, "ticket": {j.Ticket}}
		data := sessionCall(t, a, "", "GET", "/api/search/export?"+filter.Encode(), "")
		if data.Code != 200 {
			t.Fatal(data.Code, data.Body)
		}
		rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(data.Body.String(), "\ufeff"))).ReadAll()
		if err != nil || len(rows) != 2 || rows[1][1] != "B8" || rows[1][2] != "'"+long {
			t.Fatal(len(rows), err)
		}
		filter.Set("ticket", "invalid")
		if r := sessionCall(t, a, "", "GET", "/api/search/export?"+filter.Encode(), ""); r.Code != 403 {
			t.Fatal(r.Code)
		}
		body.Full = false
		body.Recoverable = false
		k := a.searchJobs[start(body)]
		checkpointWait(t, k)
		if !k.Truncated || len(k.Matches) != 500 {
			t.Fatal("quick and full results mixed", len(k.Matches))
		}
	}
}

func TestFullSearchResumesInterruptedHighHitScan(t *testing.T) {
	isolateTestStorage(t)
	file := checkpointCSV(t, 30000)
	a := checkpointOpen(t, file)
	wb := a.currentWorkbook()
	req := searchRequest{Sheet: wb.Sheets[0].Name, Scope: "current", Mode: "advanced", Query: `B:"9007199254740993.31"`, Full: true, Recoverable: true, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}
	var start struct{ JobID, TaskID string }
	if err := json.Unmarshal(checkpointCall(t, a, "/api/search/start", req, 200).Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	j := a.searchJobs[start.JobID]
	deadline := time.Now().Add(20 * time.Second)
	for j.checkpoint.lease.Snapshot().Records < 1000 {
		select {
		case <-j.done:
			t.Fatal("ended before interruption", j.Status, j.Error)
		default:
		}
		if time.Now().After(deadline) {
			j.cancel()
			t.Fatal("no confirmed records")
		}
		time.Sleep(time.Millisecond)
	}
	j.cancel()
	checkpointWait(t, j)
	m := checkpointManifest(t, start.TaskID)
	if m.Complete || m.Records < 1000 || j.Status != "cancelled" {
		t.Fatal(m.Records, j.Status)
	}
	a.Close()
	b := checkpointOpen(t, file)
	resumed := checkpointResume(t, b, m)
	checkpointWait(t, resumed)
	if resumed.Status != "done" || resumed.HitCount != 30000 {
		t.Fatal(resumed.Status, resumed.Error, resumed.HitCount)
	}
	final := checkpointManifest(t, m.ID)
	if !final.Complete || final.Records != 30000 {
		t.Fatal(final.Records)
	}
	var after int64
	count := 0
	for {
		page, err := resumed.Results.Page(t.Context(), after, searchresults.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range page.Matches {
			count++
			if h.Ref != "B"+strconv.Itoa(count+1) {
				t.Fatal(h.Ref, count)
			}
		}
		if !page.More {
			break
		}
		after = page.Next
	}
	if count != 30000 {
		t.Fatal(count)
	}
}

func TestWholeWorkbookSearchOrdersResultsAndReportsLastSheetOverflow(t *testing.T) {
	isolateTestStorage(t)
	const ns = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	const rel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	parts := map[string][]byte{
		"xl/workbook.xml":            []byte(`<workbook xmlns="` + ns + `" xmlns:r="` + rel + `"><sheets><sheet name="First" sheetId="1" r:id="s1"/><sheet name="Second" sheetId="2" r:id="s2"/></sheets></workbook>`),
		"xl/_rels/workbook.xml.rels": []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="s1" Type="` + rel + `/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="s2" Type="` + rel + `/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`),
	}
	for sheet := 1; sheet <= 2; sheet++ {
		var xml strings.Builder
		fmt.Fprintf(&xml, `<worksheet xmlns="%s"><sheetData>`, ns)
		for i := 1; i <= 300; i++ {
			fmt.Fprintf(&xml, `<row r="%d"><c r="A%d"><v>7</v></c><c r="B%d"><f>A%d+1</f><v>8</v></c></row>`, i, i, i, i)
		}
		xml.WriteString("</sheetData></worksheet>")
		parts[fmt.Sprintf("xl/worksheets/sheet%d.xml", sheet)] = []byte(xml.String())
	}
	file := filepath.Join(t.TempDir(), "two.xlsx")
	if err := os.WriteFile(file, zipArchiveFixture(t, parts), 0600); err != nil {
		t.Fatal(err)
	}
	a := checkpointOpen(t, file)
	for _, full := range []bool{false, true} {
		request := searchRequest{Sheet: "First", Query: "+1", Mode: "formula", Scope: "all", Full: full}
		var start struct{ JobID string }
		if err := json.Unmarshal(checkpointCall(t, a, "/api/search/start", request, 200).Body.Bytes(), &start); err != nil {
			t.Fatal(err)
		}
		job := a.searchJobs[start.JobID]
		checkpointWait(t, job)
		if job.Status != "done" {
			t.Fatal(job.Error)
		}
		if !full {
			if len(job.Matches) != 500 || !job.Truncated || job.Matches[299].Sheet != "First" || job.Matches[300].Sheet != "Second" {
				t.Fatal(job.Truncated, len(job.Matches))
			}
			continue
		}
		if job.HitCount != 600 {
			t.Fatal(job.HitCount)
		}
		page, err := job.Results.Page(t.Context(), 0, searchresults.Filter{Sheet: "Second", Text: "A1+1"})
		if err != nil || len(page.Matches) != 1 || page.Matches[0].Ref != "B1" {
			t.Fatal(page, err)
		}
	}
}
