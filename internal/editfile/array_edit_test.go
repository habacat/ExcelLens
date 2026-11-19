package editfile

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func TestRangeOperationsLeaveSourceIntactOnOverlapAndCancellation(t *testing.T) {
	isolateEditFileStorage(t)
	path := writeEditBook(t, func(parts map[string]string) {
		parts["xl/worksheets/sheet2.xml"] = `<worksheet xmlns="` + mainNS + `"><sheetData><row r="1"><c r="A1"><v>3</v></c><c r="B1"><v>7</v></c></row></sheetData><mergeCells count="1"><mergeCell ref="C1:D1"/></mergeCells></worksheet>`
	})
	source, err := workbook.OpenEditSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	proof, err := source.SHA256(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ kind, area, formula, want string }{
		{"mergeCells", "A1:B2", "", "请确认"},
		{"mergeCells", "A1:C2", "", "完整选择"},
		{"fixedArray", "A1:B1", "=SEQUENCE(2)", "已有内容"},
		{"fixedArray", "C1:D2", "=1", "取消"},
	} {
		var out bytes.Buffer
		op := editdoc.Structure{Sheet: "摘要", Kind: tc.kind, Range: tc.area, Formula: tc.formula}
		_, err := RewriteStructure(t.Context(), &out, source, op)
		if err == nil || !strings.Contains(err.Error(), tc.want) || out.Len() != 0 {
			t.Errorf("%s: %v (%d bytes)", tc.kind, err, out.Len())
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = RewriteStructure(ctx, &bytes.Buffer{}, source, editdoc.Structure{Kind: "fixedArray", Sheet: "摘要", Range: "E1:E3", Formula: "=3"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := VerifySource(t.Context(), source, proof); err != nil {
		t.Fatal(err)
	}
}

func TestArrayBinaryAndSpillReferenceAcrossShapeChanges(t *testing.T) {
	b := calculationFixture(t, func(parts map[string]string) {
		parts["xl/worksheets/sheet2.xml"] = `<worksheet xmlns="` + mainNS + `"><sheetData><row r="1"><c r="A1"><v>2</v></c><c r="D1"><f>SUM(_xlfn.ANCHORARRAY(B1))</f><v>0</v></c></row></sheetData></worksheet>`
	})
	a := func(col int) editdoc.Address { return editdoc.Address{Sheet: "摘要", Row: 1, Col: col} }
	patch := map[editdoc.Address]editdoc.Value{a(2): {Kind: "formula", Raw: "=_xlfn.SEQUENCE(A1)*2"}}
	for _, tc := range []struct{ rows, want string }{{"2", "6"}, {"4", "20"}, {"1", "2"}} {
		patch[a(1)] = editdoc.Value{Kind: "number", Raw: tc.rows}
		calc, err := b.Calculate(t.Context(), patch, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := calc.Cells[a(4)]; got.State != "calculated" || got.Value.Raw != tc.want {
			t.Fatalf("rows %s: %+v", tc.rows, got)
		}
	}
}
