package server

import (
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"excellens/internal/i18n"
)

type toolbarPreference struct {
	mu      sync.Mutex
	loaded  bool
	ribbon  bool
	changed chan struct{}
	closed  bool
}

func (p *toolbarPreference) waitChannel() (<-chan struct{}, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, false
	}
	if p.changed == nil {
		p.changed = make(chan struct{})
	}
	return p.changed, true
}

func (p *toolbarPreference) notify() {
	p.mu.Lock()
	if p.changed != nil {
		close(p.changed)
		p.changed = nil
	}
	p.mu.Unlock()
}

func (p *toolbarPreference) shutdown() {
	p.mu.Lock()
	p.closed = true
	if p.changed != nil {
		close(p.changed)
		p.changed = nil
	}
	p.mu.Unlock()
}

// RibbonLayout is shared by the tray and every page, across local server ports.
// Opening the default layout does not create a configuration file.
func (a *App) RibbonLayout() (bool, error) {
	pref := &a.rootApp().toolbar
	pref.mu.Lock()
	defer pref.mu.Unlock()
	if pref.loaded {
		return pref.ribbon, nil
	}
	path, err := savedConfigPath("settings", "toolbar-layout")
	if err != nil {
		return false, err
	}
	var value *bool
	found, err := readSavedConfig(path, 1024, &value)
	if err != nil {
		return false, err
	}
	if found && value == nil {
		return false, i18n.Error("service.toolbarPreference.toolbarLayoutSettingsInvalid")
	}
	pref.ribbon = value != nil && *value
	pref.loaded = true
	return pref.ribbon, nil
}

func (a *App) SetRibbonLayout(ribbon bool) error {
	pref := &a.rootApp().toolbar
	pref.mu.Lock()
	defer pref.mu.Unlock()
	if a.rootApp().restoreState.pending() {
		return i18n.Error("service.toolbarPreference.completeRestorationProcessFirst")
	}
	if pref.loaded && pref.ribbon == ribbon {
		return nil
	}
	path, err := savedConfigPath("settings", "toolbar-layout")
	if err != nil {
		return err
	}
	if err := writeSavedConfig(path, 1024, ribbon, durableConfigOps); err != nil {
		return err
	}
	pref.ribbon, pref.loaded = ribbon, true
	if pref.changed != nil {
		close(pref.changed)
		pref.changed = nil
	}
	return nil
}

const interfaceWaitTimeout = 25 * time.Second

type interfaceSettings struct {
	Ribbon         bool        `json:"ribbon"`
	CacheEpoch     uint64      `json:"cacheEpoch"`
	Language       i18n.Locale `json:"language"`
	LanguageChoice i18n.Locale `json:"languageChoice"`
}

func (a *App) readInterfaceSettings(r *http.Request) (interfaceSettings, error) {
	ribbon, err := a.RibbonLayout()
	if err != nil {
		return interfaceSettings{}, err
	}
	choice := a.DisplayLanguageChoice()
	return interfaceSettings{
		Ribbon: ribbon, CacheEpoch: a.cacheEpoch.Load(),
		Language: negotiatePageLocale(r, choice), LanguageChoice: choice,
	}, nil
}

func (a *App) handleInterfaceSettings(w http.ResponseWriter, r *http.Request) {
	root := a.rootApp()
	if root.restoreState.pending() {
		w.Header().Set("X-ExcelLens-Recovery", "required")
		writeMessage(w, http.StatusServiceUnavailable, "service.toolbarPreference.restorationTaskNotCompletedYet")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("wait") == "1" {
			a.handleInterfaceWait(w, r)
			return
		}
	case http.MethodPost:
		var req struct {
			Ribbon   *bool   `json:"ribbon"`
			Language *string `json:"language"`
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil || decodeSavedConfig(data, &req) != nil || (req.Ribbon == nil) == (req.Language == nil) {
			writeMessage(w, http.StatusBadRequest, "service.toolbarPreference.selectToolbarLayout")
			return
		}
		if req.Language != nil {
			language := i18n.Locale(*req.Language)
			if language != "" && !i18n.Supported(*req.Language) {
				writeMessage(w, http.StatusBadRequest, "service.language.invalid")
				return
			}
			if err := a.SetDisplayLanguage(language); err != nil {
				writeFailure(w, http.StatusInternalServerError, err)
				return
			}
		} else if err := a.SetRibbonLayout(*req.Ribbon); err != nil {
			writeMessage(w, http.StatusInternalServerError, "service.toolbarPreference.failedSaveToolbarLayout", i18n.Args{"detail": err})
			return
		}
	default:
		methodNotAllowed(w)
		return
	}
	settings, err := root.readInterfaceSettings(r)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "service.toolbarPreference.failedReadToolbarLayout", i18n.Args{"detail": err})
		return
	}
	writeInterfaceSettings(w, settings)
}

func (a *App) handleInterfaceWait(w http.ResponseWriter, r *http.Request) {
	root := a.rootApp()
	q := r.URL.Query()
	clientRibbon, hasRibbon := parseQueryBool(q.Get("ribbon"))
	clientEpoch, errEpoch := strconv.ParseUint(q.Get("epoch"), 10, 64)
	hasEpoch := errEpoch == nil
	clientLanguage := q.Get("language")

	// Subscribe before reading the settings to avoid missing a change.
	ch, open := root.toolbar.waitChannel()
	settings, err := root.readInterfaceSettings(r)
	if err != nil {
		writeMessage(w, http.StatusInternalServerError, "service.toolbarPreference.failedReadToolbarLayout", i18n.Args{"detail": err})
		return
	}
	if open && (!hasRibbon || settings.Ribbon == clientRibbon) && (!hasEpoch || settings.CacheEpoch == clientEpoch) &&
		(clientLanguage == "" || clientLanguage == string(settings.Language)) &&
		(!q.Has("languageChoice") || q.Get("languageChoice") == string(settings.LanguageChoice)) {
		timer := time.NewTimer(interfaceWaitTimeout)
		defer timer.Stop()
		select {
		case <-ch:
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		if r.Context().Err() != nil {
			return
		}
		if settings, err = root.readInterfaceSettings(r); err != nil {
			writeMessage(w, http.StatusInternalServerError, "service.toolbarPreference.failedReadToolbarLayout", i18n.Args{"detail": err})
			return
		}
	}
	writeInterfaceSettings(w, settings)
}

func writeInterfaceSettings(w http.ResponseWriter, settings interfaceSettings) {
	w.Header().Set("X-ExcelLens-Cache-Epoch", strconv.FormatUint(settings.CacheEpoch, 10))
	writeJSON(w, settings)
}

func parseQueryBool(value string) (bool, bool) {
	switch value {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
	}
}
