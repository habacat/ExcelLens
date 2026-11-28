package server

import (
	"errors"
	"net/http"
	"sync"

	"excellens/internal/i18n"
)

const localeConfigLimit = 128

type localePreference struct {
	mu     sync.Mutex
	loaded bool
	value  i18n.Locale
}

func (p *localePreference) get() (i18n.Locale, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return p.value, nil
	}
	path, err := savedConfigPath("settings", "interface-language")
	if err != nil {
		return "", err
	}
	var value *string
	found, err := readSavedConfig(path, localeConfigLimit, &value)
	if err != nil {
		return "", err
	}
	if found {
		if value == nil || (*value != "" && !i18n.Supported(*value)) {
			return "", errors.New("invalid interface language preference")
		}
		p.value = i18n.Locale(*value)
	}
	p.loaded = true
	return p.value, nil
}

func (p *localePreference) set(value i18n.Locale) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded && p.value == value {
		return nil
	}
	path, err := savedConfigPath("settings", "interface-language")
	if err != nil {
		return err
	}
	if err := writeSavedConfig(path, localeConfigLimit, value, durableConfigOps); err != nil {
		return err
	}
	p.value, p.loaded = value, true
	return nil
}

func (p *localePreference) invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value, p.loaded = "", false
}

func (a *App) DisplayLocale() i18n.Locale {
	if choice := a.DisplayLanguageChoice(); choice != "" {
		return choice
	}
	return i18n.System()
}

func (a *App) DisplayLanguageChoice() i18n.Locale {
	choice, _ := a.rootApp().locale.get()
	return choice
}

func (a *App) pageLocale(r *http.Request) i18n.Locale {
	return negotiatePageLocale(r, a.DisplayLanguageChoice())
}

func negotiatePageLocale(r *http.Request, choice i18n.Locale) i18n.Locale {
	if choice != "" {
		return choice
	}
	if matched := i18n.Match(r.Header.Get("Accept-Language"), ""); matched != "" {
		return matched
	}
	return i18n.System()
}

const languageHeader = "X-ExcelLens-Language"

func (a *App) requestLocale(r *http.Request) i18n.Locale {
	if value := r.Header.Get(languageHeader); i18n.Supported(value) {
		return i18n.Locale(value)
	}
	return a.pageLocale(r)
}

func (a *App) SetDisplayLanguage(language i18n.Locale) error {
	if language != "" && !i18n.Supported(string(language)) {
		return i18n.Error("service.language.invalid")
	}
	root := a.rootApp()
	root.cacheResetMu.RLock()
	defer root.cacheResetMu.RUnlock()
	if root.restoreState.pending() {
		return i18n.Error("service.language.recoveryPending")
	}
	if err := root.locale.set(language); err != nil {
		return i18n.Wrap("service.language.saveFailed", err)
	}
	root.toolbar.notify()
	return nil
}
