package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/buildinfo"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type stabilityOSMemory struct {
	PrivateBytes, WorkingSetBytes uint64
	Handles                       uint32
}

var stabilityProcessMemory = func() (stabilityOSMemory, error) { return stabilityOSMemory{}, nil }

type stabilitySample struct {
	ElapsedSeconds               float64
	Cycles                       int
	HeapBytes, HeapInuseBytes    uint64
	Goroutines, Pins, QueryFiles int
	Filters, Searches, Analyses  []int
	Exports                      []int
	Process                      stabilityOSMemory
}

type stabilitySource struct {
	mappedObserved                   bool
	app                              *App
	session, path, book, sheet, hash string
	query                            analysis.Query
	filter                           filterRequest
	exact, search                    string
	searchHits, records              int
	positions                        []int
	row                              func(int) []string
}

type stabilityHTTP struct {
	client     *http.Client
	url, token string
}

type stabilityHTTPError struct {
	path   string
	status int
	body   []byte
}

func (e *stabilityHTTPError) Error() string {
	return fmt.Sprintf("%s: HTTP %d %s", e.path, e.status, e.body)
}

func (h stabilityHTTP) call(session, method, path string, value, output any) error {
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, h.url+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-ExcelLens-Token", h.token)
	req.Header.Set("X-ExcelLens-Session", session)
	req.Header.Set("Content-Type", "application/json")
	response, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &stabilityHTTPError{path, response.StatusCode, data}
	}
	if output == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func (h stabilityHTTP) wait(session, route, id string, want string, output any) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var data json.RawMessage
		if err := h.call(session, "GET", route+"?id="+id, nil, &data); err != nil {
			return err
		}
		var state struct{ Status, Error string }
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
		if state.Status == want {
			if output != nil {
				return json.Unmarshal(data, output)
			}
			return nil
		}
		if state.Status == "error" || state.Status == "failed" || state.Status == "cancelled" {
			return fmt.Errorf("%s: %s %s", route, state.Status, state.Error)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("%s did not reach %s", route, want)
}

func stabilityHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func stabilityCSV(reader io.Reader, source *stabilitySource) error {
	buffer := bufio.NewReaderSize(reader, 64<<10)
	if prefix, _ := buffer.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = buffer.Discard(3)
	}
	rows := csv.NewReader(buffer)
	for i, position := range source.positions {
		got, err := rows.Read()
		if err != nil {
			return fmt.Errorf("export record %d: %w", i, err)
		}
		if want := source.row(position); !reflect.DeepEqual(got, want) {
			return fmt.Errorf("export record %d source row %d: got %q want %q", i, position, got, want)
		}
	}
	if _, err := rows.Read(); err != io.EOF {
		return fmt.Errorf("export has extra or malformed records: %v", err)
	}
	return nil
}

func stabilityVerifyExport(path, format string, source *stabilitySource) error {
	if format == "csv" {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		return stabilityCSV(file, source)
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	count := 0
	for _, item := range archive.File {
		if !strings.HasSuffix(item.Name, ".csv") {
			continue
		}
		count++
		stream, err := item.Open()
		if err != nil {
			return err
		}
		err = stabilityCSV(stream, source) // Read through EOF also checks standard ZIP CRC.
		closeErr := stream.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	if count != 1 {
		return fmt.Errorf("ZIP contains %d CSV members", count)
	}
	return nil
}

func (h stabilityHTTP) cycle(source *stabilitySource, cycle int) error {
	var filtered, searched, analysed struct{ JobID string }
	if err := h.call(source.session, "POST", "/api/filter/start", source.filter, &filtered); err != nil {
		return err
	}
	query := source.search
	if source.records > 4 {
		query = fmt.Sprintf("000%09d", cycle%source.records+1)
	}
	search := searchRequest{Sheet: source.sheet, WorkbookKey: source.app.currentWorkbook().Fingerprint, Book: source.book, Query: query, Mode: "value", Scope: "current"}
	if err := h.call(source.session, "POST", "/api/search/start", search, &searched); err != nil {
		return err
	}
	if err := h.call(source.session, "POST", "/api/analysis/query/start", source.query, &analysed); err != nil {
		return err
	}
	var filter struct {
		FilterID    string
		MatchedRows int
	}
	if err := h.wait(source.session, "/api/filter/status", filtered.JobID, "done", &filter); err != nil {
		return err
	}
	if filter.MatchedRows != len(source.positions) {
		return fmt.Errorf("filter returned %d rows, want %d", filter.MatchedRows, len(source.positions))
	}
	if source.records >= 350_000 {
		if !workbook.QueryFilesInUse(filepath.Join(source.app.currentWorkbook().CacheDir, "queries")) {
			return errors.New("large-source filter did not exercise mapped row-order ownership")
		}
		source.mappedObserved = true
	}
	var hits struct {
		Matches   []workbook.SearchResult
		Truncated bool
	}
	if err := h.wait(source.session, "/api/search/status", searched.JobID, "done", &hits); err != nil {
		return err
	}
	if len(hits.Matches) != source.searchHits || hits.Truncated {
		return fmt.Errorf("search returned %d hits, truncated=%v", len(hits.Matches), hits.Truncated)
	}
	var analysisResult struct{ Result analysis.Result }
	if err := h.wait(source.session, "/api/analysis/query/status", analysed.JobID, "done", &analysisResult); err != nil {
		return err
	}
	if analysisResult.Result.Grand.Exact != source.exact || analysisResult.Result.Scanned != int64(source.records) {
		return fmt.Errorf("exact analysis changed: %s / %d", analysisResult.Result.Grand.Exact, analysisResult.Result.Scanned)
	}
	format := "csv"
	if cycle%2 != 0 {
		format = "zipcsv"
	}
	var exported struct{ JobID string }
	req := exportRequest{Scope: "filtered", Sheet: source.sheet, Book: source.book, WorkbookKey: source.app.currentWorkbook().Fingerprint, FilterID: filter.FilterID, Format: format, Mode: "values", BOM: true, IncludeHidden: true}
	if err := h.call(source.session, "POST", "/api/export/start", req, &exported); err != nil {
		return err
	}
	// The accepted export must retain its own row order across the real clear.
	if err := h.call(source.session, "POST", "/api/filter/clear", map[string]string{"id": filter.FilterID}, nil); err != nil {
		return err
	}
	var exportedStatus struct {
		DownloadURL string
		DoneRows    int64
	}
	if err := h.wait(source.session, "/api/export/status", exported.JobID, "done", &exportedStatus); err != nil {
		return err
	}
	source.app.mu.RLock()
	job := source.app.exportJobs[exported.JobID]
	source.app.mu.RUnlock()
	select {
	case <-job.done:
	case <-time.After(30 * time.Second):
		return errors.New("published export did not release its worker")
	}
	if exportedStatus.DoneRows != int64(len(source.positions)) {
		return fmt.Errorf("export row count %d", exportedStatus.DoneRows)
	}
	job.mu.RLock()
	path := job.FilePath
	job.mu.RUnlock()
	if err := stabilityVerifyExport(path, format, source); err != nil {
		return err
	}
	diskHash, err := stabilityHash(path)
	if err != nil {
		return err
	}
	reqDownload, err := http.NewRequest("GET", h.url+exportedStatus.DownloadURL, nil)
	if err != nil {
		return err
	}
	reqDownload.Header.Set("X-ExcelLens-Session", source.session)
	response, err := h.client.Do(reqDownload)
	if err != nil {
		return err
	}
	downloadHash := sha256.New()
	_, readErr := io.Copy(downloadHash, response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if response.StatusCode != 200 || hex.EncodeToString(downloadHash.Sum(nil)) != diskHash {
		return errors.New("HTTP download differs from complete published export")
	}
	// Delete only this probe's verified output, never source/cache/query files.
	rel, err := filepath.Rel(ExportDir(), path)
	if err != nil || !filepath.IsLocal(rel) {
		return errors.New("probe export escaped isolated output directory")
	}
	return os.Remove(path)
}

func stabilityQuiescent(root *App, sources []*stabilitySource) error {
	for _, source := range sources {
		a := source.app
		var exits []<-chan struct{}
		a.mu.RLock()
		for _, job := range a.filterJobs {
			exits = append(exits, job.done)
		}
		for _, job := range a.searchJobs {
			exits = append(exits, job.done)
		}
		for _, job := range a.exportJobs {
			exits = append(exits, job.done)
		}
		a.mu.RUnlock()
		a.analysisMu.Lock()
		for _, job := range a.analysisJobs {
			exits = append(exits, job.done)
		}
		a.analysisMu.Unlock()
		for _, done := range exits {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				return errors.New("task did not reach resource quiescence")
			}
		}
	}
	for _, slots := range []chan struct{}{root.indexSem, root.querySem, root.analysisSem, root.exportSem, root.recoverySem} {
		if len(slots) != 0 {
			return errors.New("shared work slot remains occupied at quiescence")
		}
	}
	return nil
}

// Force a genuine queued cancellation, then switch a tab while its export is
// queued. The manually occupied slots are released even on probe failures.
func (h stabilityHTTP) lifecycle(root *App, sources []*stabilitySource) error {
	if err := stabilityQuiescent(root, sources); err != nil {
		return err
	}
	for range cap(root.querySem) {
		root.querySem <- struct{}{}
	}
	queryHeld := true
	defer func() {
		if queryHeld {
			for range cap(root.querySem) {
				<-root.querySem
			}
		}
	}()
	for _, source := range sources {
		var job struct{ JobID string }
		if err := h.call(source.session, "POST", "/api/filter/start", source.filter, &job); err != nil {
			return err
		}
		if err := h.call(source.session, "POST", "/api/filter/cancel", map[string]string{"id": job.JobID}, nil); err != nil {
			return err
		}
		if err := h.wait(source.session, "/api/filter/status", job.JobID, "cancelled", nil); err != nil {
			return err
		}
	}
	type retryIntent struct {
		source *stabilitySource
		id     string
	}
	var retries []retryIntent
	for _, source := range sources {
		request := searchRequest{Sheet: source.sheet, Book: source.book, WorkbookKey: source.app.currentWorkbook().Fingerprint, Query: "__qa_cancel_retry_" + randomID(), Mode: "value", Scope: "current"}
		var first, next struct{ JobID string }
		if err := h.call(source.session, "POST", "/api/search/start", request, &first); err != nil {
			return err
		}
		if err := h.call(source.session, "POST", "/api/search/cancel", map[string]string{"id": first.JobID}, nil); err != nil {
			return err
		}
		if err := h.call(source.session, "POST", "/api/search/start", request, &next); err != nil {
			return err
		}
		if next.JobID == first.JobID {
			return errors.New("new search intent adopted a cancelled queued task")
		}
		retries = append(retries, retryIntent{source, next.JobID})
		for _, kind := range []string{"book", "fingerprint"} {
			stale := request
			if kind == "book" {
				stale.Book = "outdated-source"
			} else {
				stale.WorkbookKey = "outdated-fingerprint"
			}
			err := h.call(source.session, "POST", "/api/search/start", stale, nil)
			var status *stabilityHTTPError
			if !errors.As(err, &status) || status.status != http.StatusConflict {
				return fmt.Errorf("stale %s request was not rejected: %v", kind, err)
			}
		}
	}
	for range cap(root.querySem) {
		<-root.querySem
	}
	queryHeld = false
	for _, retry := range retries {
		var result struct {
			Matches   []workbook.SearchResult
			Truncated bool
		}
		if err := h.wait(retry.source.session, "/api/search/status", retry.id, "done", &result); err != nil {
			return err
		}
		if len(result.Matches) != 0 || result.Truncated {
			return errors.New("retried empty search returned unexpected results")
		}
	}
	if err := stabilityQuiescent(root, sources); err != nil {
		return err
	}
	for range cap(root.exportSem) {
		root.exportSem <- struct{}{}
	}
	defer func() {
		for range cap(root.exportSem) {
			<-root.exportSem
		}
	}()
	source := sources[1]
	var queued struct{ JobID string }
	request := exportRequest{Scope: "sheet", Sheet: source.sheet, Book: source.book, WorkbookKey: source.app.currentWorkbook().Fingerprint, Format: "csv", Mode: "values"}
	if err := h.call(source.session, "POST", "/api/export/start", request, &queued); err != nil {
		return err
	}
	source.app.mu.RLock()
	job := source.app.exportJobs[queued.JobID]
	source.app.mu.RUnlock()
	if err := source.app.OpenPath(source.path); err != nil {
		return err
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		return errors.New("tab switch did not cancel queued export")
	}
	job.mu.RLock()
	cancelled := job.Status == "cancelled" && job.FilePath == ""
	job.mu.RUnlock()
	if !cancelled {
		return errors.New("queued export published after its tab switched")
	}
	wb := source.app.currentWorkbook()
	if sourceBookID(wb) != source.book {
		return errors.New("tab switch changed source identity")
	}
	_, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	return err
}

func stabilitySnapshot(root *App, sources []*stabilitySource, start time.Time, cycles int) (stabilitySample, error) {
	if err := stabilityQuiescent(root, sources); err != nil {
		return stabilitySample{}, err
	}
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	process, err := stabilityProcessMemory()
	if err != nil {
		return stabilitySample{}, err
	}
	sample := stabilitySample{ElapsedSeconds: time.Since(start).Seconds(), Cycles: cycles, HeapBytes: mem.HeapAlloc, HeapInuseBytes: mem.HeapInuse, Goroutines: runtime.NumGoroutine(), Process: process}
	root.sourceMu.Lock()
	sample.Pins = len(root.sourcePins) + len(root.sourceUploads)
	bytes := root.relationBytes + root.compositionBytes
	root.sourceMu.Unlock()
	if sample.Pins != 0 || bytes != 0 {
		return sample, fmt.Errorf("quiescent source ownership retained %d pins / %d bytes", sample.Pins, bytes)
	}
	for _, source := range sources {
		a := source.app
		a.mu.RLock()
		sample.Filters = append(sample.Filters, len(a.filterJobs))
		sample.Searches = append(sample.Searches, len(a.searchJobs))
		sample.Exports = append(sample.Exports, len(a.exportJobs))
		tooMany := len(a.filterJobs) > formats.QueryTaskHistory || len(a.searchJobs) > formats.QueryTaskHistory || len(a.exportJobs) > 26 || len(a.filterResults) != 0
		a.mu.RUnlock()
		a.analysisMu.Lock()
		sample.Analyses = append(sample.Analyses, len(a.analysisJobs))
		tooMany = tooMany || len(a.analysisJobs) > 12
		a.analysisMu.Unlock()
		if tooMany {
			return sample, errors.New("task/result history exceeded its ownership capacity")
		}
		if digest, err := stabilityHash(source.path); err != nil || digest != source.hash {
			return sample, fmt.Errorf("source hash changed: %w", err)
		}
	}
	err = filepath.WalkDir(workbook.CacheRoot(), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".query-") {
			sample.QueryFiles++
		}
		return nil
	})
	if err != nil {
		return sample, err
	}
	if sample.QueryFiles != 0 {
		return sample, fmt.Errorf("%d query temporary files remain after all owners released", sample.QueryFiles)
	}
	return sample, nil
}

func TestStabilityRealHTTPBaseline(t *testing.T) {
	if os.Getenv("EXCELLENS_STABILITY") != "1" {
		t.Skip("optional isolated long-running real HTTP/resource baseline")
	}
	smoke := os.Getenv("EXCELLENS_STABILITY_MODE") == "smoke"
	duration := 30 * time.Minute
	if raw := os.Getenv("EXCELLENS_STABILITY_DURATION"); raw != "" {
		var err error
		duration, err = time.ParseDuration(raw)
		if err != nil || duration < 30*time.Minute || duration > 2*time.Hour {
			t.Fatal("long baseline duration must be 30m through 2h")
		}
	}
	rows := 350_000
	if raw := os.Getenv("EXCELLENS_STABILITY_ROWS"); raw != "" {
		var err error
		rows, err = strconv.Atoi(raw)
		if err != nil || rows < 100 || rows > 1_000_000 || !smoke && rows < 350_000 {
			t.Fatal("long baseline needs 350000 through 1000000 CSV records")
		}
	}
	output := os.Getenv("EXCELLENS_STABILITY_REPORT")
	if output == "" {
		t.Fatal("set EXCELLENS_STABILITY_REPORT to preserve baseline evidence")
	}
	isolateTestStorage(t)
	root, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	server := httptest.NewServer(root.Handler())
	defer server.Close()
	transport := &http.Transport{MaxIdleConns: 12, MaxIdleConnsPerHost: 8, IdleConnTimeout: time.Minute}
	defer transport.CloseIdleConnections()
	httpProbe := stabilityHTTP{client: &http.Client{Transport: transport, Timeout: 90 * time.Second}, url: server.URL, token: root.token}
	csvPath := filterCheckpointCSV(t, rows)
	xlsxPath := filepath.Join(t.TempDir(), "native.xlsx")
	writeAnalysisWorkbook(t, xlsxPath)
	paths := []string{csvPath, xlsxPath}
	var sources []*stabilitySource
	for i, path := range paths {
		id := randomID()
		session, err := root.sessionFor(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := session.app.OpenPath(path); err != nil {
			t.Fatal(err)
		}
		wb := session.app.currentWorkbook()
		if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
			t.Fatal(err)
		}
		s := &stabilitySource{app: session.app, session: id, path: path, book: sourceBookID(wb), sheet: wb.Sheets[0].Name, positions: []int{1}}
		s.hash, err = stabilityHash(path)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			s.records, s.searchHits = rows, 1
			s.filter = filterCheckpointRequest(true)
			for row := 1; row <= rows; row++ {
				if row%7 != 0 {
					s.positions = append(s.positions, row+1)
				}
			}
			sort.Slice(s.positions[1:], func(i, j int) bool {
				a, b := s.positions[i+1]-1, s.positions[j+1]-1
				if a%17 != b%17 {
					return a%17 > b%17
				}
				return a < b
			})
			s.row = func(row int) []string {
				if row == 1 {
					return []string{"编号", "金额", "分组"}
				}
				n := row - 1
				return []string{fmt.Sprintf("000%09d", n), fmt.Sprintf("9007199254740993.%02d", n%17), "keep"}
			}
			base, _ := new(big.Int).SetString("900719925474099300", 10)
			base.Mul(base, big.NewInt(int64(rows)))
			fraction := int64(0)
			for n := 1; n <= rows; n++ {
				fraction += int64(n % 17)
			}
			base.Add(base, big.NewInt(fraction))
			text := base.String()
			s.exact = strings.TrimRight(strings.TrimRight(text[:len(text)-2]+"."+text[len(text)-2:], "0"), ".")
			s.query = analysis.Query{Source: "wb:" + s.sheet, HeaderRow: 1, Rows: []string{"C3"}, Value: "C2", Aggregate: "sum", Precision: 2}
		} else {
			s.records, s.search, s.searchHits, s.exact = 4, "北京", 2, "9007199254740993.31"
			s.positions = []int{1, 3, 2, 5}
			s.filter = filterRequest{HeaderRow: 1, Rules: []workbook.FilterRule{{Column: 2, Operator: "equals", Value: "债券"}}, SortKeys: []workbook.SortKey{{Column: 3, Mode: "number", Descending: true}}}
			values := map[int][]string{1: {"机构", "品种", "金额", "日期"}, 2: {"北京", "债券", "0.1", "2026-01-05"}, 3: {"北京", "债券", "0.2", "2026-01-07"}, 5: {"上海", "债券", "1e-2", "2026-03-07"}}
			s.row = func(row int) []string { return values[row] }
			s.query = analysis.Query{Source: "wb:" + s.sheet, HeaderRow: 1, Rows: []string{"C1"}, Value: "C3", Aggregate: "sum", Precision: 2}
		}
		s.filter.Sheet, s.filter.Book, s.filter.WorkbookKey = s.sheet, s.book, wb.Fingerprint
		sources = append(sources, s)
	}
	started := time.Now()
	var samples []stabilitySample
	cycles := 0
	passed := false
	lifecycleRounds := 0
	workloadStart := time.Time{}
	writeReport := func(state string) error {
		workloadSeconds := float64(0)
		if !workloadStart.IsZero() {
			workloadSeconds = time.Since(workloadStart).Seconds()
		}
		report := map[string]any{"version": buildinfo.Version, "result": state, "processId": os.Getpid(), "smokeOnly": smoke, "requiredWorkloadSeconds": duration.Seconds(), "workloadSeconds": workloadSeconds, "elapsedSeconds": time.Since(started).Seconds(), "cycles": cycles, "verifiedCompleteExports": cycles * len(sources), "queuedQueryCancellations": lifecycleRounds * len(sources) * 2, "queuedSearchRetries": lifecycleRounds * len(sources), "rejectedStaleSources": lifecycleRounds * len(sources) * 2, "queuedExportSwitches": lifecycleRounds, "csvRecords": rows, "nativeRecords": 4, "sourceSHA256": []string{sources[0].hash, sources[1].hash}, "samples": samples, "realLoopbackHTTP": true, "desktopManualAcceptance": false, "officeWpsAcceptance": false}
		report["mappedFilterObserved"] = sources[0].mappedObserved
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(output+".tmp", append(data, '\n'), 0600); err != nil {
			return err
		}
		return os.Rename(output+".tmp", output)
	}
	defer func() {
		state := "FAIL"
		if passed {
			state = "PASS"
		}
		if err := writeReport(state); err != nil {
			t.Error(err)
		}
	}()
	var baseline stabilitySample
	warmup := 8
	if smoke {
		warmup = 1
	}
	nextSample := time.Now()
	for smoke && cycles < 3 || !smoke && (workloadStart.IsZero() || time.Since(workloadStart) < duration) {
		results := make(chan error, len(sources))
		for _, source := range sources {
			go func(s *stabilitySource) { results <- httpProbe.cycle(s, cycles) }(source)
		}
		var cycleErr error
		for range sources {
			cycleErr = errors.Join(cycleErr, <-results)
		}
		if cycleErr != nil {
			t.Fatal(cycleErr)
		}
		cycles++
		if cycles%10 == 0 || smoke && cycles == 2 {
			if err := httpProbe.lifecycle(root, sources); err != nil {
				t.Fatal(err)
			}
			lifecycleRounds++
		}
		if cycles == warmup || time.Now().After(nextSample) {
			sample, err := stabilitySnapshot(root, sources, started, cycles)
			samples = append(samples, sample)
			if err != nil {
				t.Fatal(err)
			}
			if cycles == warmup {
				baseline = sample
				workloadStart = time.Now()
			}
			if cycles > warmup && (sample.HeapBytes > baseline.HeapBytes+(128<<20) || sample.Goroutines > baseline.Goroutines+32 || sample.Process.Handles > baseline.Process.Handles+64) {
				t.Fatal("quiescent resources exceeded the workload baseline guard", sample, baseline)
			}
			t.Logf("cycle=%d elapsed=%.1fs heap=%.1fMiB private=%.1fMiB handles=%d goroutines=%d histories=%v/%v", cycles, sample.ElapsedSeconds, float64(sample.HeapBytes)/(1<<20), float64(sample.Process.PrivateBytes)/(1<<20), sample.Process.Handles, sample.Goroutines, sample.Filters, sample.Searches)
			nextSample = time.Now().Add(30 * time.Second)
			if err := writeReport("RUNNING"); err != nil {
				t.Fatal(err)
			}
		}
	}
	final, err := stabilitySnapshot(root, sources, started, cycles)
	samples = append(samples, final)
	if err != nil {
		t.Fatal(err)
	}
	if !smoke && time.Since(workloadStart) < duration {
		t.Fatal("long workload ended early")
	}
	transport.CloseIdleConnections()
	server.Close()
	root.Close()
	root.sourceMu.Lock()
	remaining := len(root.sourcePins) + len(root.sourceUploads)
	root.sourceMu.Unlock()
	if remaining != 0 || len(root.sessions) != 0 {
		t.Fatal("application close retained source or browser owners", remaining, len(root.sessions))
	}
	passed = true
}
