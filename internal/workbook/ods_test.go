package workbook

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestODSRunsPreserveSourceTypesSparseCoordsAndReopen(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "财务.ods")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	entry, _ := z.Create("mimetype")
	entry.Write([]byte(odsMIME))
	entry, _ = z.Create("content.xml")
	entry.Write([]byte(`<office:document-content xmlns:office="` + odsOffice + `" xmlns:table="` + odsTable + `" xmlns:text="` + odsText + `"><office:body><office:spreadsheet><table:table table:name="台账"><table:table-row table:number-rows-repeated="800000000"><table:table-cell table:number-columns-repeated="200" office:value-type="string"><text:p>001</text:p></table:table-cell></table:table-row><table:table-row><table:table-cell office:value-type="date" office:date-value="2026-11-21"><text:p>11月21日</text:p></table:table-cell><table:table-cell office:value-type="float" office:value="9007199254740993.11"/><table:table-cell table:formula="of:=SUM([.A1:.A2])"/><table:table-cell office:value-type="string"><text:p>A<text:s text:c="2"/>B<text:tab/>C</text:p><text:p>D</text:p></table:table-cell></table:table-row></table:table><table:table table:name="隐藏" table:display="false"><table:table-row><table:table-cell table:number-columns-spanned="2" office:value-type="string"><text:p>合并</text:p></table:table-cell><table:covered-table-cell/></table:table-row></table:table></office:spreadsheet></office:body></office:document-content>`))
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for pass := 0; pass < 2; pass++ {
		w, err := OpenWorkbook(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(w.Sheets) != 2 || w.Sheets[1].State != "hidden" {
			t.Fatal(w.Sheets)
		}
		var idx *SheetIndex
		if pass == 0 {
			idx, err = w.BuildIndex(w.Sheets[0], nil, nil)
		} else {
			idx = w.GetCachedIndex(w.Sheets[0].Path)
		}
		if err != nil || idx == nil {
			t.Fatal(err, "missing index", pass)
		}
		if idx.RowCount() != 800000001 || idx.MaxCol != 200 || len(idx.odsRuns) != 2 || idx.XMLSize > 2048 || idx.FirstRows(2).RowCount() != 2 {
			t.Fatal("repeat index expanded or lost coordinates", idx.RowCount(), idx.XMLSize)
		}
		rows, err := w.ReadRowsByNumbersWithOptionsContext(ctx, idx, []int{799999999, 800000001}, 1, 4, RowReadOptions{FormatValue: true, IncludeFormula: true})
		if err != nil {
			t.Fatal(err)
		}
		a, b := rows[0].Cells, rows[1].Cells
		if len(a) != 4 || a[3].Ref != "D799999999" || a[3].Raw != "001" || a[3].Type != "inlineStr" || b[0].Raw != "2026-11-21" || b[0].Display != "11月21日" || b[0].Type != "d" || b[1].Raw != "9007199254740993.11" || b[2].Formula != "of:=SUM([.A1:.A2])" || !b[2].CacheMissing || b[3].Raw != "A  B\tC\nD" {
			t.Fatal(rows)
		}
		hits, truncated, err := w.SearchSheet(idx.FirstRows(3), "台账", "001", "value", 10000, nil, nil)
		if err != nil || len(hits) != 500 || !truncated || hits[499].Row != 3 {
			t.Fatal("search repeat", len(hits), err)
		}
		state, err := w.SearchSheetResumable(ctx, idx, "台账", "9007199254740993.11", "value", 10, SearchCheckpoint{}, func(s SearchCheckpoint) error {
			if s.Next < 800000000 {
				return errors.New("scanned individual nonmatching repeated rows")
			}
			return nil
		}, nil)
		if err != nil || !state.Complete || len(state.Matches) != 1 || state.Matches[0].Ref != "B800000001" {
			t.Fatal("search across repeated rows", state, err)
		}
		if pass == 1 {
			// The active source stays open when the user clears the cache.
			if err := os.RemoveAll(w.CacheDir); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(w.CacheDir, 0700); err != nil {
				t.Fatal(err)
			}
			w.ClearIndexes()
		}
		idx, err = w.BuildIndex(w.Sheets[1], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := w.SourceLayout(w.Sheets[1], idx)
		if err != nil || len(layout.Merges) != 1 || layout.Merges[0].EndCol != 2 {
			t.Fatal(layout, err)
		}
	}
}
