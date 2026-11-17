package archives

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func TestProvidedHTMLLedgerArchive(t *testing.T) {
	path := os.Getenv("EXCELLENS_REAL_HTML_XLS7Z")
	if path == "" {
		t.Skip("requires supplied HTML XLS archive")
	}
	isolateArchiveStorage(t)
	started := time.Now()
	a, err := Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.Members) != 1 || a.Members[0].Size != 83087122 {
		t.Fatalf("archive members %+v", a.Members)
	}
	out, member, err := a.Extract(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash := sha256.New()
	_, err = io.Copy(sourceHash, f)
	f.Close()
	if err != nil || fmt.Sprintf("%x", sourceHash.Sum(nil)) != "4c1978c4af48d6eb8ef4ca35fc90f07843891002f55018fc500c5c79f5c622be" {
		t.Fatal("different source member", err)
	}
	wb, err := workbook.OpenWorkbook(out)
	if err != nil {
		t.Fatal(err)
	}
	wb.SetDisplayName(member.Name)
	if len(wb.Sheets) != 1 || wb.Sheets[0].Name != "CUX_三栏账_科目明细账__202401-06" {
		t.Fatalf("tables became fake worksheets: %+v", wb.Sheets)
	}
	preview, err := wb.PreviewRows(context.Background(), wb.Sheets[0], 80, 40)
	if err != nil || len(preview) != 80 {
		t.Fatal("preview", len(preview), err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 75475 || idx.MaxCol != 14 {
		t.Fatalf("shape %d x %d", idx.MaxRow, idx.MaxCol)
	}
	t.Logf("archive extraction + raw SHA + metadata + preview + cold index: %s; 1 worksheet, %d x %d", time.Since(started), idx.MaxRow, idx.MaxCol)
	canonical := sha256.New()
	formulas := 0
	var initial []workbook.RowData
	for start := 0; start < len(idx.Rows); start += 1024 {
		rows, err := wb.ReadRows(idx, start, min(1024, len(idx.Rows)-start), 1, 14)
		if err != nil {
			t.Fatal(err)
		}
		if start == 0 {
			initial = rows[:80]
		}
		for j, row := range rows {
			if row.Number != start+j+1 {
				t.Fatal("row coordinate shifted")
			}
			fields := make([]string, 14)
			for _, cell := range row.Cells {
				fields[cell.Col-1] = cell.Raw
				if cell.HasFormula {
					formulas++
				}
			}
			fmt.Fprintf(canonical, "%d|", row.Number)
			for _, field := range fields {
				fmt.Fprintf(canonical, "%d:", len(field))
				io.WriteString(canonical, field)
			}
			io.WriteString(canonical, "\n")
		}
	}
	want := "5d26a677a3a1110b5c873cfeb0b19969388fbdeedbab55362050c93f51b3b1fc"
	if got := fmt.Sprintf("%x", canonical.Sum(nil)); got != want {
		t.Fatalf("canonical %s, want %s", got, want)
	}
	left, _ := json.Marshal(preview)
	right, _ := json.Marshal(initial)
	if string(left) != string(right) {
		t.Fatal("preview/index values or styles differ")
	}
	merges := wb.HTMLMerges(wb.Sheets[0])
	var ranges [][4]int
	for _, m := range merges {
		ranges = append(ranges, [4]int{m.Row, m.Col, m.EndRow, m.EndCol})
	}
	encoded, _ := json.Marshal(ranges)
	mergeDigest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	if len(merges) != 3627 || mergeDigest != "e4615cc981b16186a626d2e31338ed85d34cc4ee73ecdb6ee3d3701b9a3dca71" || formulas != 837 {
		t.Fatal("merge/literal metadata changed", len(merges), mergeDigest, formulas)
	}
	get := func(row, col int) workbook.CellData {
		for _, cell := range initial[row-1].Cells {
			if cell.Col == col {
				return cell
			}
		}
		t.Fatalf("missing R%dC%d", row, col)
		return workbook.CellData{}
	}
	if get(8, 2).Raw != "日期" || get(11, 4).Raw != "期初余额" || get(12, 6).Raw != "38,403.75" || get(12, 7).Raw != "38,403.75" || !strings.Contains(get(12, 3).Raw, "CNY") {
		t.Fatal("screenshot anchor coordinates changed")
	}
	if !get(3, 3).HasFormula || get(3, 3).Raw != "2024-01" || get(3, 3).Formula != `="2024-01"` {
		t.Fatal("literal formula result lost")
	}
	title := wb.VisualStyles[get(2, 1).Style]
	head := wb.VisualStyles[get(8, 2).Style]
	if !title.Bold || title.FontSize != 18 || title.Horizontal != "center" || !head.Bold || head.Background != "#e1f5ff" || head.BorderBottom == "" {
		t.Fatal("source nested styles lost", title, head)
	}
	t.Logf("all fields/empty positions/row coordinates SHA-256: %s; %d merges + %d known literal formulas verified", want, len(merges), formulas)
	started = time.Now()
	warm, err := workbook.OpenWorkbook(out)
	if err != nil {
		t.Fatal(err)
	}
	warm.SetDisplayName(member.Name)
	if len(warm.Sheets) != 1 || len(warm.HTMLMerges(warm.Sheets[0])) != 3627 || warm.GetCachedIndex(warm.Sheets[0].Path) == nil {
		t.Fatal("warm open lost the single sheet or layout")
	}
	t.Logf("warm metadata + cached index reopen: %s", time.Since(started))
}
