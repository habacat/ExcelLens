package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChartExportLocalBlobPreviewKeepsScriptAndObjectPolicy(t *testing.T) {
	a := &App{}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "127.0.0.1:777"
	out := httptest.NewRecorder()
	a.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })).ServeHTTP(out, request)
	directives := make(map[string]string)
	for _, text := range strings.Split(out.Header().Get("Content-Security-Policy"), ";") {
		parts := strings.Fields(text)
		if len(parts) > 1 {
			directives[parts[0]] = strings.Join(parts[1:], " ")
		}
	}
	if out.Code != 200 || !strings.Contains(directives["img-src"], "blob:") || directives["script-src"] != "'self'" || directives["connect-src"] != "'self'" || directives["object-src"] != "'none'" {
		t.Fatal("generated Blob image previews must work within the script/object restrictions", out.Code, directives)
	}
}
