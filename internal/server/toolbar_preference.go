package server

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
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
	if a.rootApp().restoreState.pending() {
		return errors.New("请先完成恢复事务")
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

func (a *App) handleInterfaceSettings(w http.ResponseWriter, r *http.Request) {
	root := a.rootApp()
	if root.restoreState.pending() {
		w.Header().Set("X-ExcelLens-Recovery", "required")
		writeError(w, http.StatusServiceUnavailable, "恢复事务尚未完成，请从本地数据重试迁入")
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
	epoch := root.cacheEpoch.Load()
	w.Header().Set("X-ExcelLens-Cache-Epoch", strconv.FormatUint(epoch, 10))
	writeJSON(w, map[string]any{"ribbon": ribbon, "cacheEpoch": epoch})
}

func (a *App) handleInterfaceWait(w http.ResponseWriter, r *http.Request) {
	root := a.rootApp()
	q := r.URL.Query()
	clientRibbon, hasRibbon := parseQueryBool(q.Get("ribbon"))
	clientEpoch, errEpoch := strconv.ParseUint(q.Get("epoch"), 10, 64)
	hasEpoch := errEpoch == nil

	// Subscribe BEFORE inspecting state so no layout or cache-epoch change can
	// slip in between the check and the select wait.
	ch, open := root.toolbar.waitChannel()
	ribbon, err := a.RibbonLayout()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取工具栏布局失败："+err.Error())
		return
	}
	epoch := root.cacheEpoch.Load()
	if open && (!hasRibbon || ribbon == clientRibbon) && (!hasEpoch || epoch == clientEpoch) {
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
		if ribbon, err = a.RibbonLayout(); err != nil {
			writeError(w, http.StatusInternalServerError, "读取工具栏布局失败："+err.Error())
			return
		}
		epoch = root.cacheEpoch.Load()
	}
	w.Header().Set("X-ExcelLens-Cache-Epoch", strconv.FormatUint(epoch, 10))
	writeJSON(w, map[string]any{"ribbon": ribbon, "cacheEpoch": epoch})
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
