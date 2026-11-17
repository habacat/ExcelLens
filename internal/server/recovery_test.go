package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"excellens/internal/recovery"
)

func TestRecoveryWorksFromHomeSurvivesServerRestartAndIsolatesTabs(t *testing.T) {
	isolateTestStorage(t)
	a, _ := New()
	defer a.Close()
	owner := "recovery-page-0000000001"
	body := fmt.Sprintf(`{"owner":%q,"kind":"calculator","revision":0,"requestId":"request-00000000000001","changes":[{"key":"0","data":{"text":"=1+2","note":"待核对","source":null}}]}`, owner)
	for i, session := range []string{owner, "recovery-page-0000000002"} {
		input := strings.ReplaceAll(body, owner, session)
		response := sessionCall(t, a, session, "POST", "/api/recovery", input)
		meta := decodeResponse[recovery.Meta](t, response)
		if meta.Revision != 1 {
			t.Fatal(i, meta)
		}
	}
	list := decodeResponse[struct{ Entries []recovery.Entry }](t, analysisRequest(t, a, "GET", "/api/recovery/list?kind=calculator", ""))
	if len(list.Entries) != 2 {
		t.Fatal("tab journals did not remain independent", list)
	}
	b, _ := New()
	defer b.Close()
	snapshot := decodeResponse[recovery.Snapshot](t, analysisRequest(t, b, "GET", "/api/recovery?kind=calculator&owner="+owner, ""))
	if snapshot.Revision != 1 || len(snapshot.Records) != 1 || !strings.Contains(string(snapshot.Records["0"]), "待核对") {
		t.Fatal(snapshot)
	}
	if response := analysisRequest(t, b, "POST", "/api/recovery", strings.Replace(body, "request-00000000000001", "request-00000000000002", 1)); response.Code != http.StatusConflict {
		t.Fatalf("stale revision overwrote data: %d %s", response.Code, response.Body.String())
	}
	// Restoring a formula only loads text: it cannot create calculator or index jobs.
	if len(b.indexJobs) != 0 || b.currentWorkbook() != nil {
		t.Fatal("recovery accessed a source workbook")
	}
}

func TestRecoveryRejectsBadSchemasAndPreservesUnicodeDrafts(t *testing.T) {
	isolateTestStorage(t)
	a, _ := New()
	defer a.Close()
	owner, sheet := "recovery-page-0000000001", "analysis-sheet-0000000001"
	draft := analysisRecovery{Book: "fingerprint", ID: sheet, Row: 9999, Col: 79, Value: strings.Repeat("😀", 4000)}
	key, _ := json.Marshal([]any{draft.Book, draft.ID, draft.Row, draft.Col})
	data, _ := json.Marshal(draft)
	req := recovery.Request{Owner: owner, Kind: "analysis", RequestID: "request-00000000000001", Changes: []recovery.Change{{Key: string(key), Data: data}}}
	body, _ := json.Marshal(req)
	decodeResponse[recovery.Meta](t, analysisRequest(t, a, "POST", "/api/recovery", string(body)))
	got := decodeResponse[recovery.Snapshot](t, analysisRequest(t, a, "GET", "/api/recovery?kind=analysis&owner="+owner, ""))
	if string(got.Records[string(key)]) != string(data) {
		t.Fatal("Unicode content changed")
	}
	for _, content := range []string{
		strings.Replace(string(body), `"revision":0,`, "", 1),
		strings.Replace(string(body), `"row":9999`, `"row":10000`, 1),
		strings.Replace(string(body), `"col":79`, `"col":80`, 1),
		strings.Replace(string(body), `"book":"fingerprint"`, `"book":"other"`, 1),
		string(body) + `{}`,
		`{"owner":"../../invalid","kind":"calculator","revision":0,"requestId":"request-00000000000001","changes":[{"key":"0","data":1}]}`,
		`{"owner":"recovery-page-0000000001","kind":"calculator","revision":0,"requestId":"request-00000000000001","changes":[{"key":"0","data":{"text":"x","result":"should not be trusted"}}]}`,
	} {
		if response := analysisRequest(t, a, "POST", "/api/recovery", content); response.Code < 400 {
			t.Fatal("invalid request accepted", content[:min(len(content), 100)])
		}
	}
	if response := analysisRequest(t, a, "POST", "/api/recovery", strings.Repeat("x", 1<<20+1)); response.Code != 400 {
		t.Fatal(response.Code)
	}
}
