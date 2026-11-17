package sourceview

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"excellens/internal/formats"
)

const AppendVersion = 1

// TableRef binds a complete source or an explicitly captured subset. Book is
// path-and-content identity; equal bytes at different paths are distinct.
// It is a descriptor, never authority to open an arbitrary filesystem path.
type TableRef struct {
	Book        string `json:"book"`
	Fingerprint string `json:"fingerprint"`
	Source      string `json:"source"`
	Stamp       string `json:"stamp"`
	HeaderRow   int    `json:"headerRow"`
	Scope       *Scope `json:"scope,omitempty"`
}

func (r TableRef) Normalized() (TableRef, error) {
	if !boundedLabel(r.Book, 128) || !boundedLabel(r.Fingerprint, 128) || !boundedLabel(r.Stamp, 128) || !boundedLabel(r.Source, 512) || r.HeaderRow < 0 || r.HeaderRow > formats.ExternalRows {
		return TableRef{}, errors.New("联合来源的身份、版本或表头无效")
	}
	switch {
	case strings.HasPrefix(r.Source, "wb:") && len(r.Source) > 3:
	case strings.HasPrefix(r.Source, "scratch:") && len(r.Source) > 8:
		if r.HeaderRow != 0 || r.Scope != nil {
			return TableRef{}, errors.New("分析表联合来源不能带源表头或源范围")
		}
	default:
		return TableRef{}, errors.New("联合来源只接受原始工作表或分析表")
	}
	if r.Scope != nil {
		if r.Scope.Fingerprint != r.Fingerprint {
			return TableRef{}, errors.New("联合来源范围与内容身份不一致")
		}
		scope, err := r.Scope.Normalized(formats.ExternalRows, formats.ExcelColumns)
		if err != nil {
			return TableRef{}, err
		}
		r.Scope = &scope
	}
	return r, nil
}

func (r TableRef) Digest() string {
	data, _ := json.Marshal(r)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

type AppendInput struct {
	Ref     TableRef `json:"ref"`
	Columns []int    `json:"columns"` // output-column order; zero explicitly means missing
}

// Append is a durable recipe, not copied source data. Inputs and columns are
// ordered. Renaming a result does not change any input value, type or address.
type Append struct {
	Version int           `json:"version"`
	Name    string        `json:"name"`
	Fields  []string      `json:"fields"`
	Inputs  []AppendInput `json:"inputs"`
}

func (a *Append) UnmarshalJSON(data []byte) error {
	if len(data) > formats.CompositionDefinitionBytes {
		return errors.New("多表追加配置超过容量")
	}
	type description Append
	var out description
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("多表追加配置包含多余内容")
	}
	*a = Append(out)
	return nil
}

// Normalized validates and deep-copies all mutable descriptors. Missing input
// columns are explicit zeros; no positional or fuzzy match is introduced here.
func (a Append) Normalized() (Append, error) {
	if a.Version != AppendVersion || !boundedLabel(a.Name, 80) || strings.TrimSpace(a.Name) != a.Name || len(a.Fields) == 0 || len(a.Fields) > formats.CompositionColumns || len(a.Inputs) < 2 || len(a.Inputs) > formats.CompositionSources {
		return Append{}, errors.New("多表追加版本、名称、字段或来源数量无效")
	}
	out := Append{Version: a.Version, Name: a.Name, Fields: slices.Clone(a.Fields), Inputs: make([]AppendInput, len(a.Inputs))}
	seenNames := make(map[string]bool, len(a.Fields))
	for _, name := range out.Fields {
		if !boundedLabel(name, 128) || strings.TrimSpace(name) != name || seenNames[name] {
			return Append{}, errors.New("请为结果字段设置非空且不重复的名称")
		}
		seenNames[name] = true
	}
	seenSources := make(map[string]bool, len(a.Inputs))
	mapped := make([]bool, len(a.Fields))
	for i, input := range a.Inputs {
		ref, err := input.Ref.Normalized()
		if err != nil {
			return Append{}, err
		}
		if len(input.Columns) != len(out.Fields) {
			return Append{}, errors.New("请逐项确认每份来源的字段对齐，缺列选择空列")
		}
		// Different stamps cannot legitimize appending the same logical source
		// twice; runtime validation will separately check the actual stamp.
		identity := ref
		identity.Stamp = ""
		key := identity.Digest()
		if seenSources[key] {
			return Append{}, errors.New("同一来源和范围不能重复追加")
		}
		seenSources[key] = true
		used := make(map[int]bool, len(input.Columns))
		for column, sourceColumn := range input.Columns {
			if sourceColumn < 0 || sourceColumn > formats.ExcelColumns || sourceColumn > 0 && used[sourceColumn] {
				return Append{}, errors.New("来源列号无效或重复映射，请核对字段对齐")
			}
			if sourceColumn > 0 {
				used[sourceColumn], mapped[column] = true, true
			}
		}
		if len(used) == 0 {
			return Append{}, errors.New("每份来源至少需要选择一个字段")
		}
		out.Inputs[i] = AppendInput{Ref: ref, Columns: slices.Clone(input.Columns)}
	}
	for _, present := range mapped {
		if !present {
			return Append{}, errors.New("结果字段没有任何来源，请选择来源列或移除该字段")
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > formats.CompositionDefinitionBytes {
		return Append{}, errors.New("多表追加配置超过容量")
	}
	return out, nil
}

func (a Append) Digest() string {
	data, _ := json.Marshal(a)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func boundedLabel(value string, runes int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= runes && !strings.ContainsAny(value, "\x00\r\n")
}
