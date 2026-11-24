package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"excellens/internal/workbook"
)

type Bookmark struct {
	ID      string `json:"id"`
	Sheet   string `json:"sheet"`
	Ref     string `json:"ref"`
	Tag     string `json:"tag"`
	Note    string `json:"note"`
	Value   string `json:"value,omitempty"`
	Formula string `json:"formula,omitempty"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

type bookmarkStore struct {
	Version int        `json:"version"`
	Items   []Bookmark `json:"items"`
}

func readBookmarks(path string) (bookmarkStore, error) {
	store := bookmarkStore{Version: 1, Items: []Bookmark{}}
	bytes, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return store, err
	}
	if len(bytes) > 4<<20 {
		return store, errors.New("本地标记文件超过大小上限")
	}
	if err := json.Unmarshal(bytes, &store); err != nil {
		return bookmarkStore{}, fmt.Errorf("标记文件损坏：%w", err)
	}
	if store.Version != 1 {
		return bookmarkStore{}, errors.New("无法读取此版本的标记文件")
	}
	if store.Items == nil {
		store.Items = []Bookmark{}
	}
	return store, nil
}
func (a *App) handleBookmarks(w http.ResponseWriter, r *http.Request) {
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, 409, "请先打开工作簿")
		return
	}
	a.bookmarkMu.Lock()
	defer a.bookmarkMu.Unlock()
	path, unlock, err := lockWorkbookSetting(r.Context(), "annotations", wb.Fingerprint)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer unlock()
	store, err := readBookmarks(path)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		sort.Slice(store.Items, func(i, j int) bool { return store.Items[i].Created > store.Items[j].Created })
		writeJSON(w, store)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		Action  string `json:"action"`
		ID      string `json:"id"`
		Sheet   string `json:"sheet"`
		Ref     string `json:"ref"`
		Tag     string `json:"tag"`
		Note    string `json:"note"`
		Value   string `json:"value"`
		Formula string `json:"formula"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&input); err != nil {
		writeError(w, 400, "标记内容无效")
		return
	}
	if input.Action == "delete" {
		found := false
		for i, b := range store.Items {
			if b.ID == input.ID {
				store.Items = append(store.Items[:i], store.Items[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			writeError(w, 404, "标记不存在")
			return
		}
	} else if input.Action == "save" {
		if _, ok := wb.FindSheet(input.Sheet); !ok {
			writeError(w, 400, "工作表不存在")
			return
		}
		col, row, err := workbook.ParseCellRef(input.Ref)
		if err != nil || col < 1 || row < 1 {
			writeError(w, 400, "无效的单元格地址")
			return
		}
		input.Ref = strings.ToUpper(strings.ReplaceAll(input.Ref, "$", ""))
		if len([]rune(input.Note)) > 800 || len(input.Value) > 1500 || len(input.Formula) > 6000 {
			writeError(w, 400, "标记或快照内容太长")
			return
		}
		switch input.Tag {
		case "普通", "待复核", "已核实", "异常", "重要":
		default:
			input.Tag = "普通"
		}
		now := time.Now().Format(time.RFC3339)
		if input.ID == "" {
			if len(store.Items) >= 1500 {
				writeError(w, 400, "每个工作簿最多保存1500个标记")
				return
			}
			store.Items = append(store.Items, Bookmark{ID: randomID(), Sheet: input.Sheet, Ref: input.Ref, Tag: input.Tag, Note: input.Note, Value: input.Value, Formula: input.Formula, Created: now, Updated: now})
		} else {
			found := false
			for i := range store.Items {
				if store.Items[i].ID != input.ID {
					continue
				}
				store.Items[i].Tag = input.Tag
				store.Items[i].Note = input.Note
				store.Items[i].Updated = now
				found = true
				break
			}
			if !found {
				writeError(w, 404, "待更新的标记不存在")
				return
			}
		}
	} else {
		writeError(w, 400, "不支持的操作")
		return
	}
	if err := writeWorkbookSetting(path, store); err != nil {
		writeError(w, 500, "保存本机标记失败："+err.Error())
		return
	}
	writeJSON(w, store)
}
