package analysis

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// User-owned work sheets and reports MUST NOT live in the disposable OOXML
// index/cache directory. Clearing caches must never erase editable analyses.
const (
	MaxSheets         = 40
	MaxReports        = 80
	MaxSheetRows      = 10000
	MaxSheetCols      = 80
	MaxWorkspaceBytes = 40 << 20
	MaxCellChars      = 4000
)

type Sheet struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Columns  []string   `json:"columns"`
	Rows     [][]string `json:"rows"`
	Updated  string     `json:"updated"`
	Revision int64      `json:"revision,omitempty"`
}
type Report struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Query   Query  `json:"query"`
	Updated string `json:"updated"`
}
type Workspace struct {
	Version        int      `json:"version"`
	Sheets         []Sheet  `json:"sheets"`
	Reports        []Report `json:"reports"`
	DashboardOrder []string `json:"dashboardOrder,omitempty"`
}

func EmptyWorkspace() Workspace { return Workspace{Version: 1, Sheets: []Sheet{}, Reports: []Report{}} }
func WorkspacePath(root, fingerprint string) string {
	return filepath.Join(root, "workspaces", fingerprint+".json")
}
func LoadWorkspace(path string) (Workspace, error) {
	w := EmptyWorkspace()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		// If a process terminated between the Windows swap steps, the
		// previous durable checkpoint is still available.
		if _, e := os.Stat(path + ".bak"); e == nil {
			return LoadWorkspace(path + ".bak")
		}
		return w, nil
	}
	if err != nil {
		return w, err
	}
	if info.Size() > MaxWorkspaceBytes {
		return w, fmt.Errorf("分析工作区文件超过安全上限")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return w, err
	}
	if err = json.Unmarshal(data, &w); err != nil {
		return EmptyWorkspace(), fmt.Errorf("分析工作区文件无效：%w", err)
	}
	if err := validateWorkspaceScopes(w); err != nil {
		return EmptyWorkspace(), err
	}
	if len(w.Sheets) > MaxSheets || len(w.Reports) > MaxReports || len(w.DashboardOrder) > MaxReports {
		return EmptyWorkspace(), errors.New("分析工作区对象数量超限")
	}
	if w.Sheets == nil {
		w.Sheets = []Sheet{}
	}
	if w.Reports == nil {
		w.Reports = []Report{}
	}
	return w, nil
}
func SaveWorkspace(path string, w Workspace) error {
	if w.Version < 1 || w.Version > 6 || len(w.Sheets) > MaxSheets || len(w.Reports) > MaxReports || len(w.DashboardOrder) > MaxReports {
		return errors.New("分析工作区内容无效")
	}
	for _, report := range w.Reports {
		if err := ValidateSource(report.Query); err != nil {
			return err
		}
		if report.Query.Composition != nil {
			w.Version = max(w.Version, 6)
		}
		for _, step := range report.Query.Transforms {
			if err := ValidateTextMapping(step); err != nil {
				return err
			}
		}
		if HasTextMappings(report.Query) {
			w.Version = max(w.Version, 5)
		}
		if err := validateLayout(report.Query); err != nil {
			return err
		}
		if report.Query.Layout == "tree" {
			w.Version = max(w.Version, 3) // Never downgrade a newer workspace.
		}
		if err := ValidateComparisons(report.Query); err != nil {
			return err
		}
		if HasComparisons(report.Query) {
			w.Version = max(w.Version, 4) // Never downgrade a workspace with newer operations.
		}
		if report.Query.SourceScope != nil {
			if err := validateSourceScope(report.Query); err != nil {
				return err
			}
			w.Version = max(w.Version, 2) // Never downgrade a workspace with tree reports.
		}
	}
	for _, s := range w.Sheets {
		if len(s.Columns) > MaxSheetCols || len(s.Rows) > MaxSheetRows {
			return errors.New("编辑表格尺寸超过安全上限")
		}
		for _, row := range s.Rows {
			if len(row) > len(s.Columns) {
				return errors.New("编辑行宽度超出表头")
			}
			for _, cell := range row {
				if !ValidCellText(cell) {
					return errors.New("编辑单元格文本过长")
				}
			}
		}
	}
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	if len(data) > MaxWorkspaceBytes {
		return errors.New("分析工作区已达到40 MiB持久化限制，请拆分编辑表")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".workspace-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Retain the last fully synced version as a recovery checkpoint. Never
	// remove the user's existing workspace before a safe replacement exists.
	backup := path + ".bak"
	if previous, readErr := os.ReadFile(path); readErr == nil {
		if err = writeWorkspaceCheckpoint(backup, previous); err != nil {
			return err
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	if err = os.Rename(f.Name(), path); err == nil {
		return nil
	}
	// os.Rename destination replacement can fail on some Windows setups.
	// Move the old file aside, then restore it on *any* failed installation.
	// A crash in between is recoverable using the .bak checkpoint above.
	old := path + ".swapping"
	if e := os.Rename(path, old); e != nil {
		return fmt.Errorf("安全替换工作区失败：%w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		if restoreErr := os.Rename(old, path); restoreErr != nil {
			return fmt.Errorf("保存失败（旧工作区保存在 %s 和 %s）：%w", old, backup, err)
		}
		return err
	}
	_ = os.Remove(old)
	return nil
}

// ValidCellText uses Unicode code points, matching the editor's character
// limit. Persistence and HTTP byte budgets remain separate protections.
func ValidCellText(s string) bool {
	return len(s) <= MaxCellChars*utf8.UTFMax && utf8.ValidString(s) && utf8.RuneCountInString(s) <= MaxCellChars
}

// writeWorkspaceCheckpoint uses a separately synced temporary file. Replacing
// an older checkpoint is best effort on Windows; the primary workspace is
// untouched on any failure.
func writeWorkspaceCheckpoint(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err == nil {
		return nil
	}
	// Use a local swap because replace-existing semantics differ on Windows.
	old := path + ".previous"
	if e := os.Rename(path, old); e != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		_ = os.Rename(old, path)
		return err
	}
	_ = os.Remove(old)
	return nil
}
func (w *Workspace) Sheet(id string) *Sheet {
	for i := range w.Sheets {
		if w.Sheets[i].ID == id {
			return &w.Sheets[i]
		}
	}
	return nil
}
func (w *Workspace) Report(id string) *Report {
	for i := range w.Reports {
		if w.Reports[i].ID == id {
			return &w.Reports[i]
		}
	}
	return nil
}
func (s *Sheet) Edit(row, col int, value string) error {
	_, err := s.ApplyEdits([]CellChange{{Row: row, Col: col, Value: value}})
	return err
}
func (s *Sheet) Paste(row, col int, rows [][]string) error {
	if row < 0 || col < 0 || len(rows) > 2000 || row+len(rows) > MaxSheetRows {
		return errors.New("粘贴范围超过限制")
	}
	writes := make([]CellChange, 0)
	for i, line := range rows {
		if len(line) > MaxSheetCols || col+len(line) > len(s.Columns) {
			return errors.New("粘贴列数超过编辑表宽度")
		}
		for j, cell := range line {
			writes = append(writes, CellChange{Row: row + i, Col: col + j, Value: cell})
		}
	}
	if len(writes) == 0 {
		return nil
	}
	_, err := s.ApplyEdits(writes)
	return err
}
func NameValid(s string) bool {
	return len(strings.TrimSpace(s)) > 0 && len([]rune(s)) <= 80 && len(s) <= 240
}
