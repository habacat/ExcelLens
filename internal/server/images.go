package server

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Image metadata requires the normal API token. A separate unpredictable
// media ticket allows a browser <img> to request binary content without
// adding the privileged application API token to resource URLs.
func (a *App) handleImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	if wb.Format != "xlsx" {
		writeJSON(w, map[string]any{"images": []any{}, "count": 0})
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeError(w, http.StatusNotFound, "找不到工作表")
		return
	}
	images, err := wb.SheetImages(meta)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "读取内嵌图片失败："+err.Error())
		return
	}
	type imageDTO struct {
		ID           int     `json:"id"`
		Name         string  `json:"name,omitempty"`
		Description  string  `json:"description,omitempty"`
		Kind         string  `json:"kind"`
		EditAs       string  `json:"editAs,omitempty"`
		HasTransform bool    `json:"hasTransform,omitempty"`
		From         any     `json:"from"`
		To           any     `json:"to,omitempty"`
		Width        float64 `json:"width,omitempty"`
		Height       float64 `json:"height,omitempty"`
		X            float64 `json:"x,omitempty"`
		Y            float64 `json:"y,omitempty"`
		Crop         any     `json:"crop"`
		Rotation     float64 `json:"rotation,omitempty"`
		FlipH        bool    `json:"flipH,omitempty"`
		FlipV        bool    `json:"flipV,omitempty"`
		Src          string  `json:"src"`
	}
	payload := make([]imageDTO, 0, len(images))
	for _, im := range images {
		src := "/api/image/content?sheet=" + url.QueryEscape(meta.Name) + "&id=" + strconv.Itoa(im.ID) + "&book=" + url.QueryEscape(wb.Fingerprint) + "&ticket=" + a.mediaTicket
		if id := r.Header.Get("X-ExcelLens-Session"); id != "" {
			src += "&session=" + url.QueryEscape(id)
		}
		payload = append(payload, imageDTO{ID: im.ID, Name: im.Name, Description: im.Description, Kind: im.Kind, EditAs: im.EditAs, HasTransform: im.HasTransform, From: im.From, To: im.To,
			Width: im.Width, Height: im.Height, X: im.X, Y: im.Y, Crop: im.Crop, Rotation: im.Rotation, FlipH: im.FlipH, FlipV: im.FlipV, Src: src})
	}
	writeJSON(w, map[string]any{"sheet": meta.Name, "images": payload, "count": len(payload)})
}

func (a *App) handleImageContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	ticket := r.URL.Query().Get("ticket")
	if len(ticket) != len(a.mediaTicket) || subtle.ConstantTimeCompare([]byte(ticket), []byte(a.mediaTicket)) != 1 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || r.URL.Query().Get("book") != wb.Fingerprint {
		http.NotFound(w, r)
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil || id < 0 || id >= 4096 {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		raw, name, err := wb.PictureRaw(meta, id)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "图片下载失败："+err.Error())
			return
		}
		// The file name originates in a ZIP part, not from the HTTP client.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", path.Base(name)))
		w.Header().Set("Cache-Control", "no-store")
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(raw))
		return
	}
	data, mime, err := wb.PictureBytes(meta, id)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "图片不可预览："+err.Error())
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"ExcelLens-image-%d.%s\"", id, strings.ReplaceAll(strings.TrimPrefix(mime, "image/"), "svg+xml", "svg")))
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if mime == "image/svg+xml" {
		// SVG is rendered as an image only; never allow executable SVG documents.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'none'; connect-src 'none'; object-src 'none'; base-uri 'none'; sandbox")
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}
