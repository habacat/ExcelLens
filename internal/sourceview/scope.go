package sourceview

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"excellens/internal/formats"
)

const ScopeVersion = 1

func (s Scope) Validate() error {
	_, err := s.Normalized(formats.ExternalRows, formats.ExcelColumns)
	return err
}

// Scope is a durable description of an explicitly chosen source subset. A nil
// scope means the complete source. Runtime snapshot IDs must never enter it.
// Selection visual positions are paired with original source coordinates so
// rebuilding a saved order cannot silently reinterpret an endpoint.
type Scope struct {
	Version       int               `json:"version"`
	Fingerprint   string            `json:"fingerprint"`
	Mode          string            `json:"mode"`
	Filter        *FilterSpec       `json:"filter,omitempty"`
	Visibility    *AxisViewSettings `json:"visibility,omitempty"`
	Selection     *Selection        `json:"selection,omitempty"`
	FrozenThrough int               `json:"frozenThrough,omitempty"`
}

func (s *Scope) UnmarshalJSON(data []byte) error {
	if len(data) > formats.SourceScopeBytes {
		return errors.New("分析来源范围配置超过容量")
	}
	type description Scope
	var out description
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("分析来源范围包含多余内容")
	}
	*s = Scope(out)
	return nil
}

type Selection struct {
	StartRow    int `json:"startRow"`
	EndRow      int `json:"endRow"`
	StartColumn int `json:"startColumn"`
	EndColumn   int `json:"endColumn"`
	VisualStart int `json:"visualStart,omitempty"`
	VisualEnd   int `json:"visualEnd,omitempty"`
}

// Normalized validates the common bounded representation. Real source/index,
// filter semantics and outline-key validation remain the adapter's job.
func (s Scope) Normalized(maxRows, maxCols int) (Scope, error) {
	if s.Version != ScopeVersion || s.Fingerprint == "" || len(s.Fingerprint) > 128 || maxRows < 1 || maxRows > formats.ExternalRows || maxCols < 1 || maxCols > formats.ExcelColumns || s.FrozenThrough < 0 || s.FrozenThrough > 30 {
		return Scope{}, errors.New("分析来源范围版本、来源或容量无效")
	}
	switch s.Mode {
	case "filtered":
		if s.Filter == nil || s.Visibility != nil || s.Selection != nil || s.FrozenThrough != 0 {
			return Scope{}, errors.New("完整筛选来源范围无效")
		}
	case "visible":
		if s.Visibility == nil || s.Selection != nil {
			return Scope{}, errors.New("可见行来源范围无效")
		}
	case "selection":
		if s.Selection == nil {
			return Scope{}, errors.New("画面选区来源缺少范围")
		}
	default:
		return Scope{}, errors.New("分析来源范围类型不支持")
	}
	if s.Filter != nil {
		f := *s.Filter
		if len(f.Rules) > formats.FilterRuleCount || len(f.SortKeys) > formats.SortKeyCount || f.HeaderRow < 0 || f.HeaderRow > maxRows || f.SortColumn < 0 || f.SortColumn > maxCols || len(f.Rules) == 0 && len(f.SortKeys) == 0 && f.SortColumn == 0 {
			return Scope{}, errors.New("来源筛选规则或排序数量无效")
		}
		f.Rules = append([]FilterRule{}, f.Rules...)
		f.SortKeys = append([]SortKey{}, f.SortKeys...)
		for i, r := range f.Rules {
			if r.Column < 1 || r.Column > maxCols || len(r.Values) > formats.FilterSetValues {
				return Scope{}, errors.New("来源筛选字段或值数量无效")
			}
			f.Rules[i].Values = append([]string(nil), r.Values...)
		}
		for _, k := range f.SortKeys {
			if k.Column < 1 || k.Column > maxCols {
				return Scope{}, errors.New("来源排序字段无效")
			}
		}
		s.Filter = &f
	}
	if s.Visibility != nil {
		v, err := s.Visibility.Normalized(maxRows)
		if err != nil {
			return Scope{}, err
		}
		s.Visibility = &v
	}
	if s.Selection != nil {
		r := *s.Selection
		if r.StartRow < 1 || r.StartRow > maxRows || r.EndRow < 1 || r.EndRow > maxRows || r.StartColumn < 1 || r.StartColumn > maxCols || r.EndColumn < r.StartColumn || r.EndColumn > maxCols || r.VisualStart < 0 || r.VisualEnd < 0 || r.VisualStart > maxRows || r.VisualEnd > maxRows || (r.VisualStart == 0) != (r.VisualEnd == 0) || s.Filter == nil && (r.VisualStart != 0 || s.FrozenThrough != 0) {
			return Scope{}, errors.New("来源选区坐标无效")
		}
		s.Selection = &r
	}
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) > formats.SourceScopeBytes {
		return Scope{}, errors.New("分析来源范围配置超过容量")
	}
	return s, nil
}

func (s Scope) Digest() string {
	data, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
