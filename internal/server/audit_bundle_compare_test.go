package server

import (
	"bytes"
	"encoding/csv"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/reconcile"
)

func TestBatchReportMatchesBusinessIdentityAndKeepsBothSourceCoordinates(t *testing.T) {
	isolateTestStorage(t)
	makeBundle := func(left, right string, q reconcile.Query) (*App, string) {
		a, err := New()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Close)
		path := filepath.Join(t.TempDir(), "左.csv")
		if err = os.WriteFile(path, []byte(left), 0600); err != nil {
			t.Fatal(err)
		}
		if err = a.OpenPath(path); err != nil {
			t.Fatal(err)
		}
		l := a.currentWorkbook()
		id := attachReconcileCSV(t, a, "右.csv", right)
		r := a.resolveReconcileBook(id)
		job := startReconcile(t, a, reconcileRef(t, a, sourceBookID(l), "wb:"+l.Sheets[0].Name, 1), reconcileRef(t, a, id, "wb:"+r.Sheets[0].Name, 1), q)
		start := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/export", map[string]any{"jobId": job.ID, "format": "zipcsv", "protectCSV": true}))
		return a, awaitReconcileExport(t, a, start.JobID).FilePath
	}
	before := reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 1}}, Fields: []reconcile.Pair{{Left: 2, Right: 2, Mode: "number"}}}
	after := reconcile.Query{Keys: []reconcile.Pair{{Left: 2, Right: 2}}, Fields: []reconcile.Pair{{Left: 1, Right: 1, Mode: "number"}}}
	_, old := makeBundle("ID,金额\n001,10.01\nold,4\ndup,1\ndup,2\n,7\n", "ID,金额\n001,10.00\ndup,1\n", before)
	a, fresh := makeBundle("金额,ID\n5,new\n10.02,001\n2,dup\n1,dup\n7,\n", "金额,ID\n10,001\n1,dup\n", after)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, path := range map[string]string{"baseline": old, "current": fresh} {
		part, err := form.CreateFormFile(name, "核查.zip")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	r := localTestConnection(httptest.NewRequest("POST", "/api/reconcile/batches?book="+sourceBookID(a.currentWorkbook()), &body))
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("X-ExcelLens-Token", a.token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	response := decodeResponse[struct{ JobID string }](t, w)
	j := awaitReconcileExport(t, a, response.JobID)
	f, err := os.Open(j.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := csv.NewReader(f)
	seen := map[string]int{}
	changed := false
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[row[0]]++
		if row[0] == "持续·值或状态变化" && strings.Contains(row[3], "001") {
			changed = row[6] == "B2" && row[17] == "A3" && row[11] == "0.01" && row[22] == "0.02"
		}
	}
	if !changed || seen["新增异常"] != 1 || seen["已消失异常"] != 1 || seen["当期无效主键"] != 1 || seen["基线无效主键"] != 1 || seen["持续·内容相同"] != 4 {
		t.Fatal(seen, changed)
	}
	bad := before
	bad.Fields = []reconcile.Pair{{Left: 2, Right: 2, Mode: "text"}}
	_, differentRules := makeBundle("ID,金额\n1,2\n", "ID,金额\n1,3\n", bad)
	dir := t.TempDir()
	for name, path := range map[string]string{"baseline": old, "current": differentRules} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name+".zip"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	l, err := readBatchBundle(t.Context(), dir, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	defer l.data.Close()
	rb, err := readBatchBundle(t.Context(), dir, "current")
	if err != nil {
		t.Fatal(err)
	}
	defer rb.data.Close()
	if sameBatchRules(l.record, rb.record) {
		t.Fatal("different matching modes accepted")
	}
	same := l.record
	same.Query.Fields = append([]reconcile.Pair(nil), l.record.Query.Fields...)
	l.record.Query.Fields[0].Tolerance = "0.01"
	same.Query.Fields[0].Tolerance = "0.010"
	if !sameBatchRules(l.record, same) {
		t.Fatal("numerically identical field tolerances should compare")
	}
	same.Query.Fields[0].Tolerance = "0.02"
	if sameBatchRules(l.record, same) {
		t.Fatal("changed field tolerance accepted")
	}
	same.Query.Fields[0].Tolerance = "0.01"
	same.RuleVersion = 1
	if sameBatchRules(l.record, same) {
		t.Fatal("incompatible rule versions accepted")
	}
}
