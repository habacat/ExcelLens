package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"excellens/internal/localstore"
	"excellens/internal/recovery"
)

func snapshotSavedReferences(roots localstore.Roots, key string) ([]string, error) {
	var refs []string
	for _, root := range []string{roots.Config, roots.Local} {
		marks, err := readBookmarks(filepath.Join(root, "annotations", key+".json"))
		if err != nil {
			return nil, err
		}
		if len(marks.Items) > 0 {
			refs = append(refs, "审计标记")
		}
	}
	views, err := readNamedViews(filepath.Join(roots.Config, "named-views", key+".json"), key)
	if err != nil {
		return nil, err
	}
	for _, view := range views.Items {
		refs = append(refs, "命名视图「"+view.Name+"」")
	}
	history, err := readCalculatorHistory(calculatorHistoryPath())
	if err != nil {
		return nil, err
	}
	for _, item := range history.Records {
		for _, op := range item.Operands {
			if op.WorkbookKey == key {
				refs = append(refs, "计算记录「"+item.Name+"」")
				break
			}
		}
	}
	return refs, nil
}

func (a *App) snapshotDraftReferences(ctx context.Context, key string) ([]string, error) {
	store, err := a.recoveryData()
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, kind := range []string{"calculator", "analysis"} {
		entries, err := store.ListAll(kind)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			data, err := store.Load(entry.Owner, kind, entry.Backup)
			if err != nil {
				return nil, fmt.Errorf("页面恢复记录：%w", err)
			}
			for id, raw := range data.Records {
				if err := validateRecovery(kind, recovery.Change{Key: id, Data: raw}); err != nil {
					return nil, err
				}
				if kind == "calculator" {
					var item calculatorRecovery
					if err := json.Unmarshal(raw, &item); err != nil {
						return nil, err
					}
					if item.Source != nil && item.Source.WorkbookKey == key {
						refs = append(refs, "计算表恢复记录")
						break
					}
				} else {
					var item analysisRecovery
					if err := json.Unmarshal(raw, &item); err != nil {
						return nil, err
					}
					if item.Book == key {
						refs = append(refs, "分析草稿恢复记录")
						break
					}
				}
			}
		}
	}
	return refs, nil
}
