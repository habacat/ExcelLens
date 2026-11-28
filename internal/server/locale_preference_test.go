package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"excellens/internal/i18n"
)

func TestLanguagePreferencePersistsAndWakesHomeTabs(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	call := func(a *App, url, language string) *httptest.ResponseRecorder {
		r := localTestConnection(httptest.NewRequest(http.MethodGet, url, nil))
		r.Header.Set("Accept-Language", language)
		r.Header.Set("X-ExcelLens-Token", a.token)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, r)
		return rec
	}
	checkPage := func(a *App, offeredLanguage string, expected i18n.Locale) {
		t.Helper()
		rec := call(a, "/", offeredLanguage)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Language") != string(expected) ||
			!strings.Contains(rec.Body.String(), `lang="`+string(expected)+`"`) ||
			strings.Contains(rec.Body.String(), "__LANGUAGE_MESSAGES__") {
			t.Fatalf("unexpected page status/language: %d %q", rec.Code, rec.Header().Get("Content-Language"))
		}
	}
	checkPage(a, "ja-JP,en;q=0.7", "ja")

	watched := make(chan *httptest.ResponseRecorder, 1)
	go func() { watched <- call(a, "/api/interface?wait=1&language=ja&languageChoice=", "ja-JP") }()
	if err := a.SetDisplayLanguage("ja"); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-watched:
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"languageChoice":"ja"`) {
			t.Fatalf("unchanged resolved language hid the shared preference: %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the watch did not report the changed language preference")
	}

	go func() { watched <- call(a, "/api/interface?wait=1&language=ja", "ja-JP") }()
	if err := a.SetDisplayLanguage("ru"); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-watched:
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"language":"ru"`) {
			t.Fatalf("unexpected watch response: %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("language preference did not wake the interface watch")
	}

	reopened, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	checkPage(reopened, "ja-JP", "ru")
	req := localTestConnection(httptest.NewRequest(http.MethodPost, "/api/interface", strings.NewReader(`{"language":"fr"}`)))
	req.Header.Set("X-ExcelLens-Token", reopened.token)
	req.Header.Set(languageHeader, "ja")
	rec := httptest.NewRecorder()
	reopened.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Language") != "ja" ||
		!strings.Contains(rec.Body.String(), "対応している表示言語を選択してください") ||
		!strings.Contains(rec.Body.String(), `"code":"service.language.invalid"`) {
		t.Fatalf("localized error = %d %s", rec.Code, rec.Body.String())
	}
	if err := reopened.SetDisplayLanguage("fr"); err == nil {
		t.Fatal("an unsupported preference was saved")
	}
	checkPage(reopened, "ko-KR", "ru")
	if err := reopened.SetDisplayLanguage(""); err != nil {
		t.Fatal(err)
	}
	checkPage(reopened, "zh-TW", "zh-Hant")
}
