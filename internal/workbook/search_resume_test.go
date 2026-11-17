package workbook

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func searchResumeNative(t *testing.T, records int) (*Workbook, *SheetIndex, [32]byte) {
	t.Helper()
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "resume.xlsx")
	var sheet strings.Builder
	sheet.WriteString(`<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>编号</t></is></c><c r="B1" t="inlineStr"><is><t>金额</t></is></c></row>`)
	for i := 1; i <= records; i++ {
		row := 2*i + 1
		value := fmt.Sprintf("ID%06d", i)
		if i == 100 || i == 4500 {
			value = "needleABC"
		}
		if i == 3000 {
			value = "needleabc"
		}
		fmt.Fprintf(&sheet, `<row r="%d"><c r="A%d" t="inlineStr"><is><t>%s</t></is></c><c r="B%d"><v>9007199254740993.31</v></c>`, row, row, value, row)
		switch {
		case i == 1:
			fmt.Fprintf(&sheet, `<c r="C%d"><f t="shared" si="0" ref="C3:C%d">A3+B3</f><v>1</v></c>`, row, 2*records+1)
		case i == 100 || i == 4500:
			fmt.Fprintf(&sheet, `<c r="C%d"><f>SUM(B%d)</f></c>`, row, row)
		default:
			fmt.Fprintf(&sheet, `<c r="C%d"><f t="shared" si="0"/><v>2</v></c>`, row)
		}
		sheet.WriteString(`</row>`)
	}
	sheet.WriteString(`</sheetData></worksheet>`)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   sheet.String(),
	}
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, data := range parts {
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return w, idx, sha256.Sum256(before)
}

func pauseSearch(t *testing.T, w *Workbook, idx *SheetIndex, query, mode string) (SearchCheckpoint, SearchCheckpoint) {
	t.Helper()
	want, err := w.SearchSheetResumable(context.Background(), idx, w.Sheets[0].Name, query, mode, 500, SearchCheckpoint{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("intentional complete-record interruption")
	var saved SearchCheckpoint
	_, err = w.SearchSheetResumable(context.Background(), idx, w.Sheets[0].Name, query, mode, 500, SearchCheckpoint{}, func(state SearchCheckpoint) error {
		if state.Next == formats.CheckpointSearchRows && !state.Complete {
			saved = state
			return stop
		}
		return nil
	}, nil)
	if !errors.Is(err, stop) || saved.Next != formats.CheckpointSearchRows {
		t.Fatal("did not stop on the actual complete-record boundary", saved, err)
	}
	return saved, want
}

func TestSearchResumeNativeModesRetainSparseCoordinatesSharedFormulasAndRegexCase(t *testing.T) {
	w, idx, before := searchResumeNative(t, 5000)
	for _, query := range []struct {
		mode, text string
		matches    int
	}{{"value", "needle", 3}, {"formula", "SUM", 2}, {"reference", "B9001", 1}, {"advanced", `A~"ABC"`, 2}, {"advanced", `A~"abc"`, 1}, {"advanced", `A:"needle" AND B > 0`, 3}} {
		t.Run(query.mode+query.text, func(t *testing.T) {
			saved, want := pauseSearch(t, w, idx, query.text, query.mode)
			result, err := w.SearchSheetResumable(context.Background(), idx, "Data", query.text, query.mode, 500, saved, nil, nil)
			if err != nil || !reflect.DeepEqual(result, want) || !result.Complete || result.Next != idx.RowCount() {
				t.Fatalf("resumed result differs from one uninterrupted scan\nwant=%+v\ngot=%+v\nerror=%v", want, result, err)
			}
			if len(result.Matches) != query.matches {
				t.Fatal("source predicate, regex case or result count changed", result)
			}
			if query.mode == "reference" && (len(result.Matches) != 1 || result.Matches[0].Ref != "C9001") {
				t.Fatal("shared/reference formula source coordinate changed", result)
			}
		})
	}
	after, _ := os.ReadFile(w.Path)
	if sha256.Sum256(after) != before {
		t.Fatal("search recovery modified the source")
	}
}

func TestSearchResumeCSVTSVAndHTMLUseTheSameExternalRecordCursor(t *testing.T) {
	for _, kind := range []string{"csv", "tsv", "html"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			var source strings.Builder
			separator := ","
			if kind == "tsv" {
				separator = "\t"
			}
			name := "resume." + kind
			if kind == "html" {
				name = "resume.xls"
				source.WriteString("<table><tr><th>编号</th><th>金额</th></tr>")
			} else {
				source.WriteString("编号" + separator + "金额\n")
			}
			for i := 1; i <= 5000; i++ {
				id := fmt.Sprintf("%08d", i)
				if i == 100 || i == 4500 {
					id = "needleABC"
				}
				if kind == "html" {
					fmt.Fprintf(&source, "<tr><td>%s</td><td>9007199254740993.31</td></tr>", id)
				} else {
					fmt.Fprintf(&source, "%s%s9007199254740993.31\n", id, separator)
				}
			}
			if kind == "html" {
				source.WriteString("</table>")
			}
			w, idx := openIndexText(t, name, []byte(source.String()))
			for _, mode := range []string{"value", "advanced"} {
				query := "needle"
				if mode == "advanced" {
					query = `A~"ABC"`
				}
				saved, want := pauseSearch(t, w, idx, query, mode)
				got, err := w.SearchSheetResumable(context.Background(), idx, w.Sheets[0].Name, query, mode, 500, saved, nil, nil)
				if err != nil || !reflect.DeepEqual(got, want) || len(got.Matches) != 2 {
					t.Fatal("external adapter resumed differently", mode, got, err)
				}
			}
		})
	}
}

func TestSearchResumeTerminalTruncationNeverAcknowledgesAnUnfinishedRow(t *testing.T) {
	isolateTestStorage(t)
	w, idx := openIndexText(t, "capped.csv", []byte("A,B,C\nhit,hit,hit\nend,end,end\n"))
	var saved SearchCheckpoint
	got, err := w.SearchSheetResumable(context.Background(), idx, w.Sheets[0].Name, "hit", "value", 2, SearchCheckpoint{}, func(state SearchCheckpoint) error { saved = state; return nil }, nil)
	if err != nil || !got.Complete || !got.Truncated || got.Next != 1 || len(got.Matches) != 2 || !reflect.DeepEqual(saved, got) {
		t.Fatal("half row was claimed as completely scanned", got, err)
	}
	path := idx.XMLPath
	idx.XMLPath = filepath.Join(t.TempDir(), "must-not-be-read")
	defer func() { idx.XMLPath = path }()
	if result, err := w.SearchSheetResumable(context.Background(), idx, w.Sheets[0].Name, "hit", "value", 2, saved, func(SearchCheckpoint) error { t.Fatal("terminal result was rewritten"); return nil }, nil); err != nil || !reflect.DeepEqual(result, got) {
		t.Fatal("trusted terminal state was rescanned", result, err)
	}
}

func TestSearchResumeCallbackAndInitialStateHaveIndependentOwnership(t *testing.T) {
	w, idx, _ := searchResumeNative(t, 5000)
	saved, want := pauseSearch(t, w, idx, "needle", "value")
	original := cloneSearchCheckpoint(saved)
	got, err := w.SearchSheetResumable(context.Background(), idx, "Data", "needle", "value", 500, saved, func(state SearchCheckpoint) error {
		if len(state.Matches) > 0 {
			state.Matches[0].Value = "callback mutation"
		}
		return nil
	}, nil)
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(saved, original) {
		t.Fatal("callback or resumed search overwrote another owner", got, err)
	}
}

func TestSearchResumeRejectsInvalidCursorAndCoordinatesBeforeReading(t *testing.T) {
	w, idx, _ := searchResumeNative(t, 5000)
	saved, _ := pauseSearch(t, w, idx, "needle", "value")
	for _, mutate := range []func(*SearchCheckpoint){
		func(s *SearchCheckpoint) { s.Next = -1 },
		func(s *SearchCheckpoint) { s.Next = idx.RowCount() + 1 },
		func(s *SearchCheckpoint) { s.Complete = true },
		func(s *SearchCheckpoint) { s.Truncated = true },
		func(s *SearchCheckpoint) { s.Matches[0].Sheet = "foreign" },
		func(s *SearchCheckpoint) { s.Matches[0].Ref = "B9999" },
		func(s *SearchCheckpoint) { s.Matches[0].Row = 200; s.Matches[0].Ref = "A200" },
		func(s *SearchCheckpoint) { s.Matches = append(s.Matches, s.Matches[0]) },
	} {
		state := cloneSearchCheckpoint(saved)
		mutate(&state)
		if _, err := w.SearchSheetResumable(context.Background(), idx, "Data", "needle", "value", 500, state, nil, nil); err == nil {
			t.Fatal("invalid cursor was accepted", state)
		}
	}
}

func TestSearchResumeCancellationAndCheckpointFailureDoNotBecomeSuccessfulCompletion(t *testing.T) {
	w, idx, _ := searchResumeNative(t, 5000)
	saved, _ := pauseSearch(t, w, idx, "needle", "value")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := w.SearchSheetResumable(ctx, idx, "Data", "needle", "value", 500, saved, nil, nil); !errors.Is(err, context.Canceled) || got.Complete || got.Next != saved.Next {
		t.Fatal("cancelled recovery was published", got, err)
	}
	failure := errors.New("checkpoint persistence failed")
	if _, err := w.SearchSheetResumable(context.Background(), idx, "Data", "needle", "value", 500, saved, func(SearchCheckpoint) error { return failure }, nil); !errors.Is(err, failure) {
		t.Fatal("checkpoint failure was hidden", err)
	}
}

func TestSearchResumeRejectsGrowingHitBytesBeforeNextRecordCheckpoint(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "large-matches.csv")
	value := "needle" + strings.Repeat("x", 1<<20)
	if err := os.WriteFile(file, []byte(value+"\n"+value+"\n"+value+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"value", "advanced"} {
		query := "needle"
		if mode == "advanced" {
			query = `A:"needle"`
		}
		confirmed := false
		state, err := w.SearchSheetResumable(context.Background(), idx, w.Sheets[0].Name, query, mode, 500, SearchCheckpoint{}, func(SearchCheckpoint) error { confirmed = true; return nil }, nil)
		if err == nil || !strings.Contains(err.Error(), "恢复容量") || state.Complete || len(state.Matches) != 2 || state.Next != 2 || confirmed {
			t.Fatal("large hits reached the journal/terminal limit first", mode, state.Next, len(state.Matches), state.Complete, confirmed, err)
		}
		// The reversible feature must not redefine ordinary source searches.
		ordinary, err := w.SearchSheetResumable(context.Background(), idx, w.Sheets[0].Name, query, mode, 500, SearchCheckpoint{}, nil, nil)
		if err != nil || !ordinary.Complete || len(ordinary.Matches) != 3 {
			t.Fatal("ordinary searches inherited a persistence-only capacity", mode, len(ordinary.Matches), err)
		}
	}
}
