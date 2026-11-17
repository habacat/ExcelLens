package server

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestProfilePreservesExactDecimalsAndNativeErrorKinds(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "profile.xlsx")
	var content bytes.Buffer
	z := zip.NewWriter(&content)
	for name, xml := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>` +
			`<row r="1"><c r="A1"><v>0.00000000000001</v></c><c r="B1"><v>1.1234567890123456789</v></c><c r="C1" t="inlineStr"><is><t>#N/A pending</t></is></c><c r="D1" t="e"><v>#N/A</v></c><c r="E1" t="str"><f>"#REF!"</f><v>#REF!</v></c></row>` +
			`<row r="2"><c r="A2"><v>0</v></c><c r="B2"><f>1/0</f></c><c r="C2" t="inlineStr"><is><t>00042</t></is></c></row>` +
			`</sheetData></worksheet>`,
	} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(xml)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	a := prepareFeatureApp(t, path)
	defer a.Close()
	profile := decodeResponse[profileReport](t, featureCall(t, a, "POST", "/api/profile", `{"sheet":"First","start":"A1","end":"E2"}`))
	quick := decodeResponse[selectionStatResult](t, featureCall(t, a, "POST", "/api/selection/stats", `{"sheet":"First","ranges":[{"start":"A1","end":"E2"}]}`))
	if profile.Sum != "1.1234567890123556789" || profile.Sum != quick.Sum || profile.Average != quick.Average || profile.Minimum != quick.Minimum || profile.Maximum != quick.Maximum {
		t.Fatalf("profile/status precision disagree: %+v / %+v", profile, quick)
	}
	if profile.Number != 3 || profile.Errors != 1 || len(profile.ErrorRefs) != 1 || profile.ErrorRefs[0] != "D1" || profile.CacheMissing != 1 {
		t.Fatalf("text/identifier/error/missing-cache types were confused: %+v", profile)
	}
	tiny := decodeResponse[profileReport](t, featureCall(t, a, "POST", "/api/profile", `{"sheet":"First","start":"A1","end":"A1"}`))
	if tiny.Sum != "0.00000000000001" || tiny.Minimum != tiny.Sum {
		t.Fatal("small nonzero decimal rounded to zero", tiny)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(content.Bytes(), after) {
		t.Fatal("source changed", err)
	}
}
