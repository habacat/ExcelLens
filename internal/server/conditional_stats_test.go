package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/localstore"
	"excellens/internal/workbook"
)

func writeConditionalStatsWorkbook(t *testing.T, rows int) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), fmt.Sprintf("stats-%d.xlsx", rows))
	var sheet strings.Builder
	sheet.WriteString(`<worksheet><sheetData>`)
	for r := 2; r <= rows+1; r++ {
		fmt.Fprintf(&sheet, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>K%d</t></is></c><c r="C%d"><v>2</v></c></row>`, r, r, r%8, r)
	}
	sheet.WriteString(`</sheetData>`)
	// Priority 1 on F: whole-column COUNTIF (needs disk statistics). Priority 2 on F: TRUE() (must not leak while pending).
	fmt.Fprintf(&sheet, `<conditionalFormatting sqref="F2:F%d"><cfRule type="expression" priority="1" dxfId="0"><formula>COUNTIF($A:$A,$A2)&gt;1</formula></cfRule><cfRule type="expression" priority="2" dxfId="0"><formula>TRUE()</formula></cfRule></conditionalFormatting>`, rows+1)
	// Priority 3 on G: short 3-row moving SUM (viewport cache, immediate without disk stats!).
	fmt.Fprintf(&sheet, `<conditionalFormatting sqref="G2:G%d"><cfRule type="expression" priority="3" dxfId="0"><formula>SUM($C2:$C4)=6</formula></cfRule></conditionalFormatting>`, rows+1)
	sheet.WriteString(`</worksheet>`)
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Ledger" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><fonts count="1"><font><name val="Calibri"/></font></fonts><cellXfs count="1"><xf fontId="0" numFmtId="0"/></cellXfs><dxfs count="1"><dxf><fill><patternFill patternType="solid"><fgColor rgb="FFFFFF00"/></patternFill></fill></dxf></dxfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   sheet.String(),
	})
	return file
}

func waitConditionalStatsJob(t *testing.T, a *App, jobID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res := featureCall(t, a, "GET", "/api/conditional-stats/status?id="+jobID, "")
		if res.Code != 200 {
			t.Fatalf("status failed: %d %s", res.Code, res.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		status, _ := body["status"].(string)
		if status == "done" || status == "cancelled" || status == "error" {
			return body
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("timed out waiting for conditional stats job", jobID)
	return nil
}

func TestConditionalStatsDecoupledIndexPendingAndDamagedRepair(t *testing.T) {
	isolateTestStorage(t)
	file := writeConditionalStatsWorkbook(t, 80)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}

	// 1. Row indexing completes independently without creating any conditional stats file.
	startIdx := decodeResponse[struct {
		Ready bool   `json:"ready"`
		JobID string `json:"jobId"`
	}](t, featureCall(t, a, "POST", "/api/index/start", `{"sheet":"Ledger"}`))
	if !startIdx.Ready {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			st := decodeResponse[struct {
				Status string `json:"status"`
			}](t, featureCall(t, a, "GET", "/api/index/status?id="+startIdx.JobID, ""))
			if st.Status == "done" {
				break
			}
			if st.Status == "error" || st.Status == "cancelled" {
				t.Fatal("index failed:", st.Status)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	files, _ := filepath.Glob(filepath.Join(workbook.ConditionalStatsRoot(), "*.db"))
	if len(files) != 0 {
		t.Fatalf("row index must not synchronously build conditional stats: %v", files)
	}

	// 2. Immediate foreground /api/sheet returns short-range SUM style immediately and marks whole-column COUNTIF pending without forging.
	readSheet := func(extra string) (rows []workbook.RowData, pending bool) {
		var body struct {
			Rows                    []workbook.RowData `json:"rows"`
			ConditionalStatsPending bool               `json:"conditionalStatsPending"`
		}
		res := featureCall(t, a, "GET", "/api/sheet?sheet=Ledger&row=2&rowWindow=1&limit=5&col=6&cols=2"+extra, "")
		if res.Code != 200 {
			t.Fatalf("sheet read failed: %d %s", res.Code, res.Body.String())
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Rows, body.ConditionalStatsPending
	}
	cellAt := func(rows []workbook.RowData, col int) *workbook.CellData {
		for i := range rows[0].Cells {
			if rows[0].Cells[i].Col == col {
				return &rows[0].Cells[i]
			}
		}
		return nil
	}

	rows, pending := readSheet("")
	if !pending {
		t.Fatal("expected conditionalStatsPending=true before background stats finish")
	}
	f2 := cellAt(rows, 6)
	g2 := cellAt(rows, 7)
	if f2 == nil || f2.Conditional == nil || !f2.Conditional.Pending || f2.Conditional.Matched != 0 || f2.Conditional.Visual != nil {
		t.Fatalf("F2 must be pending without forged style: %+v", f2)
	}
	if g2 == nil || g2.Conditional == nil || g2.Conditional.Pending || g2.Conditional.Matched != 1 || g2.Conditional.Notice != "" {
		t.Fatalf("G2 short moving SUM must evaluate immediately without disk stats: %+v", g2)
	}

	// Original format OFF (conditional=0) skips conditional evaluation completely.
	plainRows, plainPending := readSheet("&conditional=0")
	if plainPending || len(plainRows[0].Cells) != 0 {
		t.Fatalf("conditional=0 should skip presentation-only cells: pending=%v cells=%+v", plainPending, plainRows[0].Cells)
	}

	// 3. Start independent background conditional stats job -> completes and viewport refreshes cleanly.
	started := decodeResponse[struct {
		Needed  bool   `json:"needed"`
		Ready   bool   `json:"ready"`
		JobID   string `json:"jobId"`
		Damaged bool   `json:"damaged"`
	}](t, featureCall(t, a, "POST", "/api/conditional-stats/start", `{"sheet":"Ledger"}`))
	if !started.Needed || started.Ready || started.JobID == "" || started.Damaged {
		t.Fatalf("unexpected start response: %+v", started)
	}
	final := waitConditionalStatsJob(t, a, started.JobID)
	if final["status"] != "done" {
		t.Fatalf("background stats did not finish cleanly: %+v", final)
	}
	rows, pending = readSheet("")
	if pending {
		t.Fatal("expected conditionalStatsPending=false after completion")
	}
	f2 = cellAt(rows, 6)
	if f2 == nil || f2.Conditional == nil || f2.Conditional.Pending || f2.Conditional.Matched != 2 || f2.Conditional.Notice != "" {
		t.Fatalf("F2 must match both rules after stats complete: %+v", f2)
	}

	// 4. Corrupt the stats file to 0 bytes -> foreground read must NEVER rebuild synchronously; background start detects damaged and repairs!
	files, _ = filepath.Glob(filepath.Join(workbook.ConditionalStatsRoot(), "*.db"))
	if len(files) != 1 {
		t.Fatalf("expected 1 stats file, got %v", files)
	}
	if err := os.WriteFile(files[0], nil, 0600); err != nil {
		t.Fatal(err)
	}
	rows, pending = readSheet("")
	if !pending {
		t.Fatal("expected conditionalStatsPending=true on zero-byte damaged file")
	}
	f2 = cellAt(rows, 6)
	if f2 == nil || f2.Conditional == nil || !f2.Conditional.Pending || !strings.Contains(f2.Conditional.Notice, "重建中") {
		t.Fatalf("expected rebuilding pending notice on damaged file: %+v", f2)
	}
	if st, err := os.Stat(files[0]); err != nil || st.Size() != 0 {
		t.Fatalf("foreground read must not rebuild damaged file synchronously: %v %v", st, err)
	}
	repair := decodeResponse[struct {
		Needed  bool   `json:"needed"`
		Ready   bool   `json:"ready"`
		JobID   string `json:"jobId"`
		Damaged bool   `json:"damaged"`
	}](t, featureCall(t, a, "POST", "/api/conditional-stats/start", `{"sheet":"Ledger"}`))
	if !repair.Needed || repair.Ready || !repair.Damaged || repair.JobID == "" {
		t.Fatalf("expected damaged rebuild job, got %+v", repair)
	}
	if st := waitConditionalStatsJob(t, a, repair.JobID); st["status"] != "done" {
		t.Fatalf("damaged rebuild failed: %+v", st)
	}
	rows, pending = readSheet("")
	if pending || cellAt(rows, 6).Conditional.Matched != 2 {
		t.Fatalf("expected full styles after background repair: pending=%v cell=%+v", pending, cellAt(rows, 6))
	}
}

func TestStorageConditionalStatsInventoryBudgetAndActiveProtection(t *testing.T) {
	isolateTestStorage(t)
	file1 := writeConditionalStatsWorkbook(t, 64)
	file2 := writeConditionalStatsWorkbook(t, 72)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	buildFor := func(path string) string {
		if err := a.OpenPath(path); err != nil {
			t.Fatal(err)
		}
		wb := a.currentWorkbook()
		idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := wb.PrepareConditionalStatistics(t.Context(), wb.Sheets[0], idx, nil); err != nil {
			t.Fatal(err)
		}
		return wb.Fingerprint
	}

	fp1 := buildFor(file1)
	fp2 := buildFor(file2) // file2 is now active; file1 is idle!

	var storage struct {
		Groups           []localstore.Usage `json:"groups"`
		ConditionalStats struct {
			BudgetBytes    int64 `json:"budgetBytes"`
			UsedBytes      int64 `json:"usedBytes"`
			Files          int   `json:"files"`
			ProtectedFiles int   `json:"protectedFiles"`
		} `json:"conditionalStats"`
	}
	res := featureCall(t, a, "GET", "/api/storage", "")
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	if err := json.Unmarshal(res.Body.Bytes(), &storage); err != nil {
		t.Fatal(err)
	}
	foundGroup := false
	for _, g := range storage.Groups {
		if g.Name == "条件统计缓存" && g.Kind == "可重建缓存" && g.Files == 2 && g.Bytes > 0 {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Fatalf("missing dedicated conditional stats storage group: %+v", storage.Groups)
	}
	if storage.ConditionalStats.BudgetBytes != formats.ConditionalStatsDefaultBudgetBytes ||
		storage.ConditionalStats.Files != 2 || storage.ConditionalStats.ProtectedFiles != 1 {
		t.Fatalf("unexpected conditionalStats summary: %+v", storage.ConditionalStats)
	}

	// Invalid budget is rejected.
	if bad := sessionCall(t, a, "", "POST", "/api/storage", `{"action":"set-conditional-stats-budget","budgetBytes":1024}`); bad.Code != 409 {
		t.Fatalf("expected invalid budget rejection, got %d %s", bad.Code, bad.Body.String())
	}

	// Valid budget persists.
	okBudget := featureCall(t, a, "POST", "/api/storage", fmt.Sprintf(`{"action":"set-conditional-stats-budget","budgetBytes":%d}`, 128<<20))
	if okBudget.Code != 200 {
		t.Fatalf("set budget failed: %d %s", okBudget.Code, okBudget.Body.String())
	}
	if got := loadConditionalStatsBudget(); got != 128<<20 {
		t.Fatalf("budget did not persist: got %d", got)
	}

	// Clear unused conditional stats removes idle fp1 while protecting active fp2!
	cleared := featureCall(t, a, "POST", "/api/storage", `{"action":"clear-conditional-stats"}`)
	if cleared.Code != 200 {
		t.Fatalf("clear conditional stats failed: %d %s", cleared.Code, cleared.Body.String())
	}
	f1, _ := filepath.Glob(filepath.Join(workbook.ConditionalStatsRoot(), workbook.ConditionalStatsFilePrefix(fp1)+"*.db"))
	f2, _ := filepath.Glob(filepath.Join(workbook.ConditionalStatsRoot(), workbook.ConditionalStatsFilePrefix(fp2)+"*.db"))
	if len(f1) != 0 || len(f2) != 1 {
		t.Fatalf("expected idle fp1 cleared and active fp2 kept: fp1=%v fp2=%v", f1, f2)
	}
}
