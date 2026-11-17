package server

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePictureServerWorkbook(t *testing.T, file string) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	parts := map[string]string{
		"xl/workbook.xml":                     `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels":          `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":            `<worksheet xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData><drawing r:id="draw"/></worksheet>`,
		"xl/worksheets/_rels/sheet1.xml.rels": `<Relationships><Relationship Id="draw" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/drawing" Target="../drawings/drawing1.xml"/></Relationships>`,
		"xl/drawings/drawing1.xml":            `<xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><xdr:oneCellAnchor><xdr:from><xdr:col>0</xdr:col><xdr:row>0</xdr:row></xdr:from><xdr:ext cx="952500" cy="476250"/><xdr:pic><xdr:blipFill><a:blip r:embed="pic"/></xdr:blipFill></xdr:pic><xdr:clientData/></xdr:oneCellAnchor></xdr:wsDr>`,
		"xl/drawings/_rels/drawing1.xml.rels": `<Relationships><Relationship Id="pic" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/p.png"/></Relationships>`,
	}
	for name, body := range parts {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	img, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO9ZbuYAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	w, err := z.Create("xl/media/p.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(img); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestImageEndpointAuthAndBytes(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "picture.xlsx")
	writePictureServerWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	defer a.Close()
	response := featureCall(t, a, "GET", "/api/images?sheet=First", "")
	var result struct {
		Count  int `json:"count"`
		Images []struct {
			ID    int     `json:"id"`
			Kind  string  `json:"kind"`
			Src   string  `json:"src"`
			Width float64 `json:"width"`
		} `json:"images"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(result.Images) != 1 || result.Images[0].Width != 100 || result.Images[0].Kind != "oneCell" {
		t.Fatalf("bad image response %+v", result)
	}
	src := result.Images[0].Src
	if !strings.HasPrefix(src, "/api/image/content?") || strings.Contains(src, a.token) {
		t.Fatalf("unsafe media src %s", src)
	}
	raw := httptest.NewRequest("GET", src, nil)
	raw.RemoteAddr = "127.0.0.1:6000"
	content := httptest.NewRecorder()
	a.Handler().ServeHTTP(content, raw)
	if content.Code != 200 || content.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(content.Body.String(), "\x89PNG") {
		t.Fatalf("media HTTP %d content type %s", content.Code, content.Header().Get("Content-Type"))
	}
	cases := []struct {
		name, uri string
		status    int
	}{
		{"missing media ticket", "/api/image/content?sheet=First&id=0", 403},
		{"wrong ticket", strings.Replace(src, a.mediaTicket, "wrong", 1), 403},
		{"wrong sheet", strings.Replace(src, "sheet=First", "sheet=Other", 1), 404},
		{"old workbook", strings.Replace(src, "book="+a.currentWorkbook().Fingerprint, "book=stale", 1), 404},
		{"invalid picture id", strings.Replace(src, "id=0", "id=-1", 1), 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", c.uri, nil)
			req.RemoteAddr = "127.0.0.1:6000"
			r := httptest.NewRecorder()
			a.Handler().ServeHTTP(r, req)
			if r.Code != c.status {
				t.Fatalf("HTTP %d want %d: %s", r.Code, c.status, r.Body.String())
			}
		})
	}
	download := httptest.NewRequest("GET", src+"&download=1", nil)
	download.RemoteAddr = "127.0.0.1:6000"
	exported := httptest.NewRecorder()
	a.Handler().ServeHTTP(exported, download)
	if exported.Code != 200 || exported.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(exported.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download failed: %d %+v", exported.Code, exported.Header())
	}
	unauth := httptest.NewRequest(http.MethodGet, "/api/images?sheet="+url.QueryEscape("First"), nil)
	unauth.RemoteAddr = "127.0.0.1:6000"
	denied := httptest.NewRecorder()
	a.Handler().ServeHTTP(denied, unauth)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("metadata leaked without API token: %d", denied.Code)
	}
}
