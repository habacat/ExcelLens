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
	"excellens/internal/i18n"
	"excellens/internal/recovery"
)

func (a *App) recoveryData() (*recovery.Store, error) {
	root := a.rootApp()
	root.recoveryOnce.Do(func() {
		base, err := os.UserConfigDir()
		if err != nil || base == "" {
			root.recoveryErr = i18n.Error("service.recovery.unableLocateUserConfigurationDirectory")
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
		return i18n.Error("service.recovery.recoveryContentContainsRedundantData")
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
			return i18n.Error("service.recovery.rowNumberCalculationTableInvalid")
		}
		var v calculatorRecovery
		if decodeRecovery(c.Data, &v) != nil || v.Text == nil || v.Note == nil || !utf8.ValidString(*v.Text) || !utf8.ValidString(*v.Note) ||
			len(utf16.Encode([]rune(*v.Text))) > 4096 || len(utf16.Encode([]rune(*v.Note))) > 4096 {
			return i18n.Error("service.recovery.calculatedTableRestorationContentInvalid")
		}
		if v.Source != nil && (len(v.Source.WorkbookKey) > 128 || len(v.Source.Sheet) > 1024 || !utf8.ValidString(v.Source.Sheet)) {
			return i18n.Error("service.recovery.invalidTableSource")
		}
		return nil
	}
	if kind == "analysis" {
		var v analysisRecovery
		if decodeRecovery(c.Data, &v) != nil || len(v.Book) == 0 || len(v.Book) > 128 || !validSessionID(v.ID) ||
			v.Row < 0 || v.Row >= analysis.MaxSheetRows || v.Col < 0 || v.Col >= analysis.MaxSheetCols || !analysis.ValidCellText(v.Value) {
			return i18n.Error("service.recovery.analysisDraftContentInvalidOr")
		}
		key, _ := json.Marshal([]any{v.Book, v.ID, v.Row, v.Col})
		if c.Key != string(key) {
			return i18n.Error("service.recovery.draftCoordinatesAnalysisDoNot")
		}
		return nil
	}
	return i18n.Error("service.recovery.invalidRestoreType")
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
		writeMessage(w, http.StatusTooManyRequests, "service.recovery.restoreSaveBusyTryAgain")
		return
	}
	store, err := a.recoveryData()
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		data, err := store.Load(q.Get("owner"), q.Get("kind"), q.Get("backup") == "1")
		if err != nil {
			writeFailure(w, 422, err)
			return
		}
		for key, value := range data.Records {
			if err := validateRecovery(q.Get("kind"), recovery.Change{Key: key, Data: value}); err != nil {
				writeMessage(w, 422, "service.recovery.recoveredCopyContentNotCompatible")
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
		writeMessage(w, 400, "service.recovery.saveRecoveryRequestInvalidOr")
		return
	}
	req := recovery.Request{Owner: wire.Owner, Kind: wire.Kind, Revision: *wire.Revision, RequestID: wire.RequestID, Changes: wire.Changes}
	for _, c := range req.Changes {
		if err := validateRecovery(req.Kind, c); err != nil {
			writeFailure(w, 400, err)
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
		writeFailure(w, status, err)
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
		writeFailure(w, 500, err)
		return
	}
	list, err := store.List(r.URL.Query().Get("kind"))
	if err != nil {
		writeFailure(w, 422, err)
		return
	}
	writeJSON(w, map[string]any{"entries": list})
}
