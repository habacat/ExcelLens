package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"excellens/internal/calculator"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

type calculatorSnapshot struct {
	Name       string              `json:"name"`
	Note       string              `json:"note,omitempty"`
	Expression string              `json:"expression"`
	Precision  int                 `json:"precision"`
	Percent    bool                `json:"percent"`
	Operands   []calculatorOperand `json:"operands"`
}
type savedCalculation struct {
	calculatorSnapshot
	ID        string            `json:"id"`
	CreatedAt string            `json:"createdAt"`
	Result    calculator.Result `json:"result"`
}
type calculatorHistoryFile struct {
	Version int                `json:"version"`
	Records []savedCalculation `json:"records"`
}

var calculatorHistoryMu sync.Mutex

func calculatorHistoryPath() string {
	return filepath.Join(workbook.DataRoot(), "calculator", "history.json")
}
func readCalculatorHistory(path string) (calculatorHistoryFile, error) {
	result := calculatorHistoryFile{Version: 1, Records: []savedCalculation{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		f, err = os.Open(path + ".backup")
	}
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return result, err
	}
	if len(data) > 4<<20 || json.Unmarshal(data, &result) != nil || result.Version != 1 || len(result.Records) > 50 {
		return calculatorHistoryFile{}, i18n.Error("service.calculatorHistory.localCalculationHistoryCannotBe")
	}
	return result, nil
}
func writeCalculatorHistory(path string, value calculatorHistoryFile) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	for len(data) > 4<<20 && len(value.Records) > 1 {
		value.Records = value.Records[:len(value.Records)-1]
		data, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	if len(data) > 4<<20 {
		return i18n.Error("service.calculatorHistory.calculatedRecordsTooLargeCopy")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	backup := path + ".backup"
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(path, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(f.Name(), path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	_ = os.Remove(backup)
	return nil
}
func validateCalculatorSnapshot(snapshot calculatorSnapshot) error {
	if utf8.RuneCountInString(snapshot.Name) > 80 || utf8.RuneCountInString(snapshot.Note) > 800 || len(snapshot.Expression) > calculator.MaxExpression || len(snapshot.Operands) > calculator.MaxOperands {
		return i18n.Error("service.calculatorHistory.calculationNameRemarksOrData")
	}
	ids := map[string]bool{}
	for _, op := range snapshot.Operands {
		n, e := strconv.Atoi(op.ID)
		if e != nil || n < 1 || n > 999999 || ids[op.ID] {
			return i18n.Error("service.calculatorHistory.dataExtractionNumberInvalidOr")
		}
		ids[op.ID] = true
		if len(op.Raw) > 4096 || len(op.Display) > 4096 || len(op.File) > 8192 || len(op.Sheet) > 2048 || len(op.Detail) > 32768 || len(op.Notice) > 16384 || len(op.CapturedAt) > 64 || len(op.Ref) > 32 || len(op.WorkbookKey) > 128 || len(op.Ranges) > 256 {
			return i18n.Error("service.calculatorHistory.dataExtractionRecordTooLong")
		}
		if op.Kind != "cell" && op.Kind != "sum" && op.Kind != "manual" && op.Kind != "result" {
			return i18n.Error("service.calculatorHistory.invalidDataRetrievalCategory")
		}
		if _, err := calculator.Canonical(op.Value); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) handleCalculatorHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	calculatorHistoryMu.Lock()
	defer calculatorHistoryMu.Unlock()
	path := calculatorHistoryPath()
	history, err := readCalculatorHistory(path)
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	if r.Method == http.MethodGet {
		if id := r.URL.Query().Get("id"); id != "" {
			for _, record := range history.Records {
				if record.ID == id {
					writeJSON(w, record)
					return
				}
			}
			writeMessage(w, 404, "service.calculatorHistory.calculatedRecordsExpiredOrDo")
			return
		}
		items := make([]map[string]any, 0, len(history.Records))
		for _, record := range history.Records {
			items = append(items, map[string]any{"id": record.ID, "name": record.Name, "expression": record.Expression, "display": record.Result.Display, "createdAt": record.CreatedAt})
		}
		writeJSON(w, map[string]any{"items": items, "limit": 50})
		return
	}
	var snapshot calculatorSnapshot
	if decodeCalculator(w, r, &snapshot) != nil {
		writeMessage(w, 400, "service.calculatorHistory.invalidCalculationRecordFormat")
		return
	}
	if err := validateCalculatorSnapshot(snapshot); err != nil {
		writeFailure(w, 400, err)
		return
	}
	values := map[string]string{}
	for _, op := range snapshot.Operands {
		values[op.ID] = op.Value
	}
	result, err := calculator.Evaluate(r.Context(), calculator.Request{Expression: snapshot.Expression, Operands: values, Precision: snapshot.Precision, Percent: snapshot.Percent})
	if err != nil {
		writeFailure(w, 422, err)
		return
	}
	used := map[string]bool{}
	for _, id := range result.Used {
		used[id] = true
	}
	operands := make([]calculatorOperand, 0, len(used))
	for _, op := range snapshot.Operands {
		if used[op.ID] {
			operands = append(operands, op)
		}
	}
	snapshot.Operands = operands
	if snapshot.Name == "" {
		snapshot.Name = "未命名计算"
	}
	canonical, _ := json.Marshal(snapshot)
	hash := sha256.Sum256(canonical)
	id := hex.EncodeToString(hash[:16])
	for _, record := range history.Records {
		if record.ID == id {
			writeJSON(w, map[string]any{"id": id, "saved": true, "createdAt": record.CreatedAt})
			return
		}
	}
	record := savedCalculation{calculatorSnapshot: snapshot, ID: id, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: result}
	history.Records = append([]savedCalculation{record}, history.Records...)
	if len(history.Records) > 50 {
		history.Records = history.Records[:50]
	}
	if err := writeCalculatorHistory(path, history); err != nil {
		writeMessage(w, 500, "service.calculatorHistory.failedSaveCalculationRecords", i18n.Args{"detail": err})
		return
	}
	writeJSON(w, map[string]any{"id": id, "saved": true, "createdAt": record.CreatedAt})
}
