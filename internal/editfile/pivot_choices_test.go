package editfile

import (
	"context"
	"strconv"
	"testing"

	"excellens/internal/editdoc"
)

func BenchmarkPivotPaging32(b *testing.B) {
	book := &CalculationBook{cells: make(map[editdoc.Address]calcCell)}
	info := PivotInfo{Fields: []string{"ID"}, SourceSheet: "Data", SourceRef: "A1:A16385"}
	for row := 2; row <= 16385; row++ {
		book.cells[editdoc.Address{Sheet: "Data", Row: row, Col: 1}] = calcCell{value: editdoc.Value{Kind: "text", Raw: "Item " + strconv.Itoa(row)}}
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		list, err := book.NewPivotItemList(&Calculation{}, nil, info, "ID")
		if err != nil {
			b.Fatal(err)
		}
		for page := 0; page < 32; page++ {
			got, err := list.Page(ctx, "", page*128)
			if err != nil || len(got.Items) != 128 {
				b.Fatal(page, err)
			}
		}
	}
}

func TestPivotItemPagingSharesReadsAndRetainsTypedIdentity(t *testing.T) {
	book := &CalculationBook{cells: make(map[editdoc.Address]calcCell)}
	info := PivotInfo{Fields: []string{"Field"}, SourceSheet: "Data", SourceRef: "A1:A301"}
	set := func(row int, v editdoc.Value) {
		book.cells[editdoc.Address{Sheet: "Data", Row: row, Col: 1}] = calcCell{value: v}
	}
	for row := 2; row <= 301; row++ {
		set(row, editdoc.Value{Kind: "number", Raw: strconv.Itoa(row)})
	}
	set(3, editdoc.Value{Kind: "text", Raw: "2"})
	set(4, editdoc.Value{Kind: "boolean", Raw: "TRUE"})
	set(5, editdoc.Value{Kind: "number", Raw: "2"})
	list, err := book.NewPivotItemList(&Calculation{}, nil, info, "Field")
	if err != nil {
		t.Fatal(err)
	}
	first, err := list.Page(t.Context(), "", 0)
	if err != nil || !first.More || first.Items[0].Type != "n" || first.Items[1].Type != "s" || list.nextRow != 132 {
		t.Fatal(first, err, list.nextRow)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = list.Page(ctx, "", 128); err != context.Canceled || list.nextRow != 132 {
		t.Fatal("canceled page advanced", err)
	}
	next, err := list.Page(t.Context(), "", 128)
	if err != nil || next.Items[0].Value != "131" || list.nextRow != 260 {
		t.Fatal(next, err, list.nextRow)
	}
	filtered, err := list.Page(t.Context(), "TrUe", 0)
	if err != nil || len(filtered.Items) != 1 || filtered.Items[0].Type != "b" || filtered.More {
		t.Fatal(filtered, err)
	}
	again, err := list.Page(t.Context(), "", 0)
	if err != nil || !again.More || again.Items[2].Type != "b" || list.nextRow != 302 {
		t.Fatal(again, err, list.nextRow)
	}
}
