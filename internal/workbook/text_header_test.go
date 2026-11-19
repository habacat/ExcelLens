package workbook

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestTXTHeaderRecognitionUsesParsedRecordsAndKeepsAllRows(t *testing.T) {
	isolateTestStorage(t)
	cases := []struct {
		name, text string
		row        int
		names      []string
	}{
		{"journal", "账号@金额@日期\n00001@1.2500001@2026-11-10\n00002@2.30@2026-11-11\n", 1, []string{"账号", "金额", "日期"}},
		{"all-text", "姓名|城市\n张三|北京\n李四|上海\n", 1, []string{"姓名", "城市"}},
		{"typed-transition", "alpha\tbeta\tgamma\n001\t1.25\t2026-11-10\n002\t2.50\t2026-11-11\n", 1, []string{"alpha", "beta", "gamma"}},
		{"preamble", "审计台账\n账号|金额|摘要\n00001|1.25|收款\n00002|2.50|付款\n", 2, []string{"账号", "金额", "摘要"}},
		{"quoted-data", "id,amount,description\n00001,1.25,\"两行\n摘要\"\n00002,2.50,\"含,逗号\"\n", 1, []string{"id", "amount", "description"}},
		{"no-header", "00001@1.25@收款\n00002@2.50@付款\n00003@3.75@调整\n", 0, nil},
		{"ambiguous-text", "北京|张三\n上海|李四\n广州|王五\n", 0, nil},
		{"duplicate-labels", "金额@金额@账号\n1@2@00001\n3@4@00002\n", 0, nil},
		{"single-column", "普通文本\n日志记录\n另一行\n", 0, nil},
		{"inconsistent-shape", "账号@金额\n00001@1.25\n00002@2.50@多一列\n", 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wb, idx := openIndexText(t, tc.name+".txt", []byte(tc.text))
			header := wb.SourceInfo.TableHeader
			if tc.row == 0 {
				if header != nil {
					t.Fatalf("ambiguous/data row identified as header: %+v", header)
				}
			} else {
				if header == nil || header.Row != tc.row || !header.Detected || !reflect.DeepEqual(header.Columns, tc.names) {
					t.Fatalf("header %+v want %v row %d", header, tc.names, tc.row)
				}
				row, err := wb.ReadTextTableHeader(context.Background(), wb.Sheets[0], tc.row)
				if err != nil || row.Detected || !reflect.DeepEqual(row.Columns, tc.names) {
					t.Fatalf("indexed header %+v: %v", row, err)
				}
			}
			var reference [][]string
			if err := wb.streamTextRows(func(fields []string) error {
				reference = append(reference, append([]string(nil), fields...))
				return nil
			}, nil); err != nil {
				t.Fatal(err)
			}
			if len(idx.ResidentRows) != len(reference) {
				t.Fatal("header detection changed source row count")
			}
			for i, fields := range reference {
				for col, value := range fields {
					if got := cellValue(t, wb, idx, i+1, col+1); got != value {
						t.Fatalf("cell R%dC%d changed", i+1, col+1)
					}
				}
			}
			if got, err := os.ReadFile(wb.Path); err != nil || string(got) != tc.text {
				t.Fatal("source modified", err)
			}
		})
	}
}

func TestJoinedOracleAndFixedWidthHeadersAreRecognized(t *testing.T) {
	isolateTestStorage(t)
	header, records := oracleJournalFixture()
	wb, idx := openIndexText(t, "joined.txt", []byte(header+strings.Join(records, "\n")+"\n"))
	if h := wb.SourceInfo.TableHeader; h == nil || h.Row != 1 || len(h.Columns) != 39 || h.Columns[38] != "seg12_desc" {
		t.Fatalf("joined header not separated from record: %+v", h)
	}
	if idx.MaxRow != 4 || cellValue(t, wb, idx, 2, 3) != "000000000000000001" {
		t.Fatal("joined header changed records")
	}
	fixed, _ := openIndexText(t, "fixed.txt", []byte("ACCOUNT      AMOUNT\n---------- ----------\n00001            1.25\n00002            2.50\n"))
	if fixed.SourceInfo.TableHeader == nil || fixed.SourceInfo.TableHeader.Row != 1 || len(fixed.SourceInfo.TableHeader.Columns) != 2 {
		t.Fatalf("fixed header not recognized: %+v", fixed.SourceInfo)
	}
}

func TestExplicitTXTHeaderCanBeChangedWithoutReindexing(t *testing.T) {
	isolateTestStorage(t)
	wb, idx := openIndexText(t, "manual.txt", []byte("说明文字\n人员@地址\n张三@北京\n李四@上海\n"))
	got, err := wb.ReadTextTableHeader(context.Background(), wb.Sheets[0], 2)
	if err != nil || got.Row != 2 || got.Detected || !reflect.DeepEqual(got.Columns, []string{"人员", "地址"}) {
		t.Fatal(got, err)
	}
	for _, bad := range []int{-1, 0, idx.MaxRow + 1} {
		if _, err := wb.ReadTextTableHeader(context.Background(), wb.Sheets[0], bad); err == nil {
			t.Fatalf("invalid header row %d accepted", bad)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := wb.ReadTextTableHeader(ctx, wb.Sheets[0], 2); err == nil {
		t.Fatal("cancelled header read accepted")
	}
	if wb.GetCachedIndex(wb.Sheets[0].Path) != idx {
		t.Fatal("presentation rebuilt the source index")
	}
}
