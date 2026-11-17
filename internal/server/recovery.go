package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"excellens/internal/analysis"
	"excellens/internal/recovery"
)

func (a *App) recoveryData() (*recovery.Store, error) {
	root := a.rootApp()
	root.recoveryOnce.Do(func() {
		base, err := os.UserConfigDir()
		if err != nil || base == "" {
			root.recoveryErr = errors.New("无法定位用户配置目录，恢复副本尚未保存")
			return
		}
		root.recoveryStore = recovery.New(filepath.Join(base, "ExcelLens", "recovery"))
	})
	return root.recoveryStore, root.recoveryErr
}

type calculatorRecovery struct {
	Text   *string `json:"text"`
	Note   *string `json:"note"`
	Source *struct {
		WorkbookKey string `json:"workbookKey"`
		Sheet       string `json:"sheet"`
	} `json:"source"`
}

type analysisRecovery struct {
	Book  string `json:"book"`
	ID    string `json:"id"`
	Row   int    `json:"row"`
	Col   int    `json:"col"`
	Value string `json:"value"`
}

func decodeRecovery(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("恢复内容包含多余数据")
	}
	return nil
}

func validateRecovery(kind string, c recovery.Change) error {
	if len(c.Data) == 0 || bytes.Equal(bytes.TrimSpace(c.Data), []byte("null")) {
		return nil
	}
	if kind == "calculator" {
		index, err := strconv.ParseUint(c.Key, 10, 53)
		if err != nil || index > recovery.MaxRevision-2 || strconv.FormatUint(index, 10) != c.Key {
			return errors.New("计算表恢复行号无效")
		}
		var v calculatorRecovery
		if decodeRecovery(c.Data, &v) != nil || v.Text == nil || v.Note == nil || !utf8.ValidString(*v.Text) || !utf8.ValidString(*v.Note) ||
			len(utf16.Encode([]rune(*v.Text))) > 4096 || len(utf16.Encode([]rune(*v.Note))) > 4096 {
			return errors.New("计算表恢复内容无效或超过4096字符")
		}
		if v.Source != nil && (len(v.Source.WorkbookKey) > 128 || len(v.Source.Sheet) > 1024 || !utf8.ValidString(v.Source.Sheet)) {
			return errors.New("计算表来源无效")
		}
		return nil
	}
	if kind == "analysis" {
		var v analysisRecovery
		if decodeRecovery(c.Data, &v) != nil || len(v.Book) == 0 || len(v.Book) > 128 || !validSessionID(v.ID) ||
			v.Row < 0 || v.Row >= analysis.MaxSheetRows || v.Col < 0 || v.Col >= analysis.MaxSheetCols || !analysis.ValidCellText(v.Value) {
			return errors.New("分析草稿恢复内容无效或超出编辑表容量")
		}
		key, _ := json.Marshal([]any{v.Book, v.ID, v.Row, v.Col})
		if c.Key != string(key) {
			return errors.New("分析草稿坐标与内容不一致")
		}
		return nil
	}
	return errors.New("恢复类型无效")
}

func (a *App) handleRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	// Recovery I/O has a separate bounded queue; it cannot take a grid scan slot.
	select {
	case a.rootApp().recoverySem <- struct{}{}:
		defer func() { <-a.rootApp().recoverySem }()
	default:
		writeError(w, http.StatusTooManyRequests, "恢复保存繁忙，请稍后重试")
		return
	}
	store, err := a.recoveryData()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		data, err := store.Load(q.Get("owner"), q.Get("kind"), q.Get("backup") == "1")
		if err != nil {
			writeError(w, 422, err.Error())
			return
		}
		for key, value := range data.Records {
			if err := validateRecovery(q.Get("kind"), recovery.Change{Key: key, Data: value}); err != nil {
				writeError(w, 422, "恢复副本内容与当前规则不兼容，请选择其他副本")
				return
			}
		}
		writeJSON(w, data)
		return
	}
	var wire struct {
		Owner     string            `json:"owner"`
		Kind      string            `json:"kind"`
		Revision  *uint64           `json:"revision"`
		RequestID string            `json:"requestId"`
		Changes   []recovery.Change `json:"changes"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil || !utf8.Valid(data) || decodeRecovery(data, &wire) != nil || wire.Revision == nil {
		writeError(w, 400, "恢复保存请求无效或超过1 MiB")
		return
	}
	req := recovery.Request{Owner: wire.Owner, Kind: wire.Kind, Revision: *wire.Revision, RequestID: wire.RequestID, Changes: wire.Changes}
	for _, c := range req.Changes {
		if err := validateRecovery(req.Kind, c); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	meta, err := store.Update(req)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, recovery.ErrConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, recovery.ErrCapacity) {
			status = http.StatusRequestEntityTooLarge
		} else {
			var pathErr *os.PathError
			var linkErr *os.LinkError
			if errors.As(err, &pathErr) || errors.As(err, &linkErr) {
				status = http.StatusServiceUnavailable
			}
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, meta)
}

func (a *App) handleRecoveryList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	store, err := a.recoveryData()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	list, err := store.List(r.URL.Query().Get("kind"))
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	writeJSON(w, map[string]any{"entries": list})
}
