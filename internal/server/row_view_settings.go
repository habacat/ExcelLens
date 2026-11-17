package server

import (
	"errors"
	"slices"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// This is a versioned user configuration, never a disposable query cache.
// One revision covers the whole file, including updates from another tab.
type rowViewSettingsStore struct {
	Version     int                                  `json:"version"`
	Fingerprint string                               `json:"fingerprint"`
	Revision    int64                                `json:"revision"`
	Sheets      map[string]workbook.AxisViewSettings `json:"sheets"`
}

func rowViewSettingsPath(wb *workbook.Workbook) (string, error) {
	return savedConfigPath("row-views", wb.Fingerprint)
}

func readRowViewSettings(path string, wb *workbook.Workbook) (rowViewSettingsStore, error) {
	out := rowViewSettingsStore{Version: 1, Fingerprint: wb.Fingerprint, Sheets: make(map[string]workbook.AxisViewSettings)}
	found, err := readSavedConfig(path, formats.RowViewConfigBytes, &out)
	if err != nil || !found {
		return out, err
	}
	if out.Version != 1 || out.Fingerprint != wb.Fingerprint || out.Revision < 0 || out.Revision >= maxConfigRevision || out.Sheets == nil || len(out.Sheets) > formats.RowViewSavedSheets {
		return out, errors.New("行显隐设置版本、来源或容量无效，原文件已保留")
	}
	source, ok := formats.Lookup(wb.Format)
	if !ok || source.MaxRows < 1 {
		return out, errors.New("行显隐设置的源格式无效")
	}
	paths := make(map[string]bool, len(wb.Sheets))
	for _, sheet := range wb.Sheets {
		paths[sheet.Path] = true
	}
	for path, settings := range out.Sheets {
		if !paths[path] {
			return out, errors.New("行显隐设置包含未知工作表，原文件已保留")
		}
		clean, err := settings.Normalized(source.MaxRows)
		if err != nil {
			return out, err
		}
		out.Sheets[path] = clean
	}
	return out, nil
}

type rowViewChange struct {
	Action string                        `json:"action"`
	First  int                           `json:"first,omitempty"`
	Last   int                           `json:"last,omitempty"`
	Hidden bool                          `json:"hidden,omitempty"`
	Group  *workbook.AxisOutlineOverride `json:"group,omitempty"`
	Depth  *uint8                        `json:"depth,omitempty"`
}

func applyRowViewChange(settings workbook.AxisViewSettings, maximum int, change rowViewChange) (workbook.AxisViewSettings, error) {
	out, err := settings.Normalized(maximum)
	if err != nil {
		return out, err
	}
	switch change.Action {
	case "original", "show-all", "hide-all":
		out = workbook.AxisViewSettings{Mode: change.Action}
	case "range":
		return out.WithRange(maximum, change.First, change.Last, change.Hidden)
	case "depth":
		if change.Depth == nil {
			return out, errors.New("请选择大纲展开层级")
		}
		depth := *change.Depth
		out.Depth, out.Groups = &depth, nil
	case "group":
		if change.Group == nil {
			return out, errors.New("请选择要展开或折叠的大纲组")
		}
		out.Groups = slices.DeleteFunc(out.Groups, func(g workbook.AxisOutlineOverride) bool { return g.AxisOutlineKey == change.Group.AxisOutlineKey })
		out.Groups = append(out.Groups, *change.Group)
	default:
		return out, errors.New("行显隐操作无效")
	}
	return out.Normalized(maximum)
}
