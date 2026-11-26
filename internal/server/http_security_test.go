package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Recorder requests do not have the local socket metadata that net/http
// supplies on real connections.
func localTestConnection(r *http.Request) *http.Request {
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 32123}
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, addr))
	r.RemoteAddr = "127.0.0.1:54321"
	r.Host = addr.String()
	return r
}

func TestLocalListenerAppliesOriginChecksBeforeDispatch(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	listener, address, err := a.Listen()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Serve(listener) }()
	defer func() {
		a.Shutdown()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for _, tc := range []struct {
		method, ref string
		anyTarget   bool
		status      int
	}{
		{http.MethodGet, address + "?tab=1", false, 200},
		{http.MethodGet, "https://untrusted.invalid/", false, 403},
		{http.MethodOptions, "https://untrusted.invalid/", true, 403},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Referer", tc.ref)
		if tc.anyTarget {
			req.URL.Path = "*"
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != tc.status || strings.Contains(string(body), a.token) != (tc.status == 200) {
			t.Errorf("%s: unexpected status %d or response body", tc.method, resp.StatusCode)
		}
	}
}

func TestLocalHTTPBoundary(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	handler := a.Handler()
	for _, tc := range []struct {
		name, host, origin, site string
		allow                    bool
	}{
		{"native launch", "127.0.0.1:32123", "", "none", true},
		{"local client", "127.0.0.1:32123", "", "", true},
		{"browser", "localhost:32123", "http://localhost:32123", "same-origin", true},
		{"unrecognized host on local socket", "untrusted.invalid:32123", "", "", false},
		{"different port", "127.0.0.1:32124", "", "", false},
		{"missing port", "localhost", "", "", false},
		{"missing host", "", "", "", false},
		{"different origin", "127.0.0.1:32123", "http://untrusted.invalid", "", false},
		{"different local origin", "127.0.0.1:32123", "http://localhost:32123", "", false},
		{"opaque origin", "localhost:32123", "null", "", false},
		{"cross-site navigation", "localhost:32123", "", "cross-site", false},
		{"cross-port site", "localhost:32123", "", "same-site", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/", "/api/status", "/assets/app.js", "/api/image/content", "/api/export/download", "/api/edits/download"} {
				if tc.allow && path != "/" && path != "/api/status" {
					continue
				}
				r := localTestConnection(httptest.NewRequest("GET", path, nil))
				r.Host = tc.host
				r.Header.Set("X-Forwarded-Host", "127.0.0.1:32123")
				r.Header.Set("X-ExcelLens-Token", a.token)
				if tc.origin != "" {
					r.Header.Set("Origin", tc.origin)
				}
				if tc.site != "" {
					r.Header.Set("Sec-Fetch-Site", tc.site)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				want := http.StatusForbidden
				if tc.allow {
					want = http.StatusOK
				}
				if w.Code != want || strings.Contains(w.Body.String(), a.token) != (tc.allow && path == "/") {
					t.Errorf("%s: status %d, unexpected status or token visibility", path, w.Code)
				}
			}
		})
	}
	r := httptest.NewRequest("GET", "http://127.0.0.1:32123/", nil)
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("missing socket metadata accepted")
	}
}

func TestControlEndpointsRejectOversizedBodies(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	handler := a.Handler()
	oversized := `{"id":"` + strings.Repeat("a", 16<<10) + `","sheet":"` + strings.Repeat("s", 16<<10) + `"}`
	for _, endpoint := range []string{
		"/api/index/start",
		"/api/index/cancel",
		"/api/search/cancel",
		"/api/filter/cancel",
		"/api/filter/clear",
		"/api/export/cancel",
	} {
		for _, body := range []string{oversized, "{}" + strings.Repeat(" ", 16<<10), "{}{}"} {
			r := localTestConnection(httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)))
			r.Header.Set("X-ExcelLens-Token", a.token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "无效") {
				t.Errorf("%s accepted an invalid control request: status=%d body=%s", endpoint, w.Code, w.Body.String())
			}
		}
	}
}
