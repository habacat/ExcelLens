package server

import (
	"net/http"

	"excellens/internal/i18n"
)

type localizedResponse struct {
	http.ResponseWriter
	locale i18n.Locale
}

func (w localizedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func responseLocale(w http.ResponseWriter) i18n.Locale {
	if localized, ok := w.(localizedResponse); ok {
		return localized.locale
	}
	return i18n.Default
}

func writeMessage(w http.ResponseWriter, status int, key i18n.Key, args ...i18n.Args) {
	writeFailure(w, status, i18n.Error(key, args...))
}

func writeFailure(w http.ResponseWriter, status int, err error) {
	locale := responseLocale(w)
	w.Header().Set("Content-Language", string(locale))
	writeJSONStatus(w, status, struct {
		Error string   `json:"error"`
		Code  i18n.Key `json:"code,omitempty"`
	}{Error: locale.ErrorText(err), Code: i18n.ErrorKey(err)})
}
