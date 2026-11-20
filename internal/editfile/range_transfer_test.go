package editfile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"excellens/internal/editdoc"
)

func TestTransferFormulaScopesAndIntersection(t *testing.T) {
	m, err := newRangeTransfer(editdoc.Structure{Kind: "cutRange", Sheet: "Origin", Range: "A1:B2", DestinationSheet: "To Space", Destination: "D4"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ scope, after, body, want string }{
		{"Origin", "To Space", "A1+$F8+'To Space'!D4", "D4+Origin!$F8+'To Space'!#REF!"},
		{"Other", "Other", "SUM(Origin!$A$1:B2,Origin!A1:A3,Origin!B:B)", "SUM('To Space'!$D$4:E5,Origin!A3:A3,Origin!B:B)"},
		{"Origin", "Origin", "IF(A2>0,\"A2\",Table1[A2])", "IF('To Space'!D5>0,\"A2\",Table1[A2])"},
	}
	for _, c := range cases {
		got, err := m.formula(c.body, c.scope, c.after)
		if err != nil || got != c.want {
			t.Errorf("%s => %s, want %s (%v)", c.body, got, c.want, err)
		}
	}
}

func TestTransferOverlappingMergeAndPartialTarget(t *testing.T) {
	m, err := newRangeTransfer(editdoc.Structure{Kind: "cutRange", Sheet: "Sheet", Range: "B2:D4", DestinationSheet: "Sheet", Destination: "C2"})
	if err != nil {
		t.Fatal(err)
	}
	merge, _ := parseRect("B2:C2")
	src, dst, err := m.mergeLayout([]cellRect{merge}, []cellRect{merge}, true)
	if err != nil || len(src) != 0 || len(dst) != 0 {
		t.Fatal(src, dst, err)
	}
	m.cut = false
	if _, _, err = m.mergeLayout([]cellRect{merge}, []cellRect{merge}, true); err == nil {
		t.Fatal("partial destination merge accepted")
	}
	m.target, _ = parseRect("F2:K7")
	_, dst, err = m.mergeLayout([]cellRect{merge}, nil, true)
	if err != nil || len(dst) != 4 {
		t.Fatal(dst, err)
	}
}

func TestCutDestinationExpandsBeforeTakingMergeOwnership(t *testing.T) {
	m, err := newRangeTransfer(editdoc.Structure{Kind: "cutRange", Sheet: "Sheet", Range: "B2:H6", DestinationSheet: "Sheet", Destination: "B4"})
	if err != nil {
		t.Fatal(err)
	}
	var merges []cellRect
	for _, ref := range []string{"B2:C4", "D3:E3", "F4:G4", "H2:H5", "K1:L1"} {
		r, _ := parseRect(ref)
		merges = append(merges, r)
	}
	_, after, err := m.mergeLayout(merges, merges, true)
	if err != nil || len(after) != 1 || after[0] != merges[4] {
		t.Fatalf("result=%v err=%v", after, err)
	}
}

// The oracle contains Excel's exact saved expressions. Unlisted operands were
// also tested in Excel and retained their original expressions.
func TestTransferExcelReferenceMatrix(t *testing.T) {
	path := os.Getenv("EXCELLENS_TRANSFER_MATRIX")
	if path == "" {
		t.Skip("set EXCELLENS_TRANSFER_MATRIX to output of tools/qa/transfer_reference_excel.py")
	}
	var oracle struct {
		Formulas []string
		Samples  []struct {
			Source, Destination string
			Changes             map[string]string
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &oracle); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Formulas) != 4050 || len(oracle.Samples) == 0 {
		t.Fatal("incomplete oracle")
	}
	for _, s := range oracle.Samples {
		t.Run(s.Source+"_"+s.Destination, func(t *testing.T) {
			sheet, dest, _ := strings.Cut(s.Destination, "!")
			m, err := newRangeTransfer(editdoc.Structure{Kind: "cutRange", Sheet: "Origin", Range: s.Source, DestinationSheet: sheet, Destination: dest})
			if err != nil {
				t.Fatal(err)
			}
			for _, old := range oracle.Formulas {
				want := old
				if changed, ok := s.Changes[old]; ok {
					want = changed
				}
				got, err := m.formula(old, "Audit", "Audit")
				if err != nil || got != want {
					t.Fatalf("%s => %s, want %s (%v)", old, got, want, err)
				}
			}
		})
	}
}
