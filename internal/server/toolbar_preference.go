package server

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
)

type toolbarPreference struct {
	mu     sync.Mutex
	loaded bool
	ribbon bool
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
		return false, errors.New("工具栏布局设置无效")
	}
	pref.ribbon = value != nil && *value
	pref.loaded = true
	return pref.ribbon, nil
}

func (a *App) SetRibbonLayout(ribbon bool) error {
	pref := &a.rootApp().toolbar
	pref.mu.Lock()
	defer pref.mu.Unlock()
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
	return nil
}

func (a *App) handleInterfaceSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-ExcelLens-Cache-Epoch", strconv.FormatUint(a.rootApp().cacheEpoch.Load(), 10))
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var req struct {
			Ribbon *bool `json:"ribbon"`
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
		if err != nil || decodeSavedConfig(data, &req) != nil || req.Ribbon == nil {
			writeError(w, http.StatusBadRequest, "请选择工具栏布局")
			return
		}
		if err := a.SetRibbonLayout(*req.Ribbon); err != nil {
			writeError(w, http.StatusInternalServerError, "保存工具栏布局失败："+err.Error())
			return
		}
	default:
		methodNotAllowed(w)
		return
	}
	ribbon, err := a.RibbonLayout()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取工具栏布局失败："+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ribbon": ribbon, "cacheEpoch": a.rootApp().cacheEpoch.Load()})
}
