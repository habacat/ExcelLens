package workbook

import (
	"encoding/json"
	"errors"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// A stored property describes source metadata, not a computed print layout.
// Missing map entries are absent; unknown/invalid fields never gain defaults.
// Values remain text so exact physical measures and source enum codes survive.
type SourcePrintProperty struct {
	Type   string `json:"type"`
	Value  string `json:"value"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type SourcePrintDefinition struct {
	Kind         string            `json:"kind"`
	Namespace    string            `json:"namespace,omitempty"`
	Attributes   map[string]string `json:"attributes,omitempty"`
	RawXML       string            `json:"rawXml,omitempty"`
	RecordID     uint16            `json:"recordId,omitempty"`
	RecordHex    string            `json:"recordHex,omitempty"`
	Continuation []string          `json:"continuation,omitempty"`
}

// Index is the zero-based first row below / column right of a saved break.
// Min/Max retain inclusive, zero-based cross-axis coordinates (OOXML schema
// defaults are zero). Attributes distinguish absent and saved values. They are
// not expanded into source cells; Index -1 denotes a missing/invalid boundary.
type SourcePrintBreak struct {
	Index      int               `json:"index"`
	Min        int               `json:"min"`
	Max        int               `json:"max"`
	Manual     *bool             `json:"manual,omitempty"`
	Pivot      *bool             `json:"pivot,omitempty"`
	Status     string            `json:"status"`
	Reason     string            `json:"reason,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type SourcePrintName struct {
	Kind       string          `json:"kind"`
	ID         string          `json:"id"`
	Scope      string          `json:"scope,omitempty"`
	ScopeID    string          `json:"scopeId,omitempty"`
	Definition string          `json:"definition"`
	TokenHex   string          `json:"tokenHex,omitempty"`
	Status     string          `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Areas      []CatalogueArea `json:"areas,omitempty"`
}

type SourcePrintReference struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Target     string `json:"target,omitempty"`
	TargetMode string `json:"targetMode,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
}

type SourcePrintMetadata struct {
	Sheet         string                                    `json:"sheet"`
	Format        string                                    `json:"format"`
	Complete      bool                                      `json:"complete"`
	NamesComplete bool                                      `json:"namesComplete"`
	Properties    map[string]map[string]SourcePrintProperty `json:"properties"`
	Names         []SourcePrintName                         `json:"names"`
	RowBreaks     []SourcePrintBreak                        `json:"rowBreaks"`
	ColBreaks     []SourcePrintBreak                        `json:"colBreaks"`
	Definitions   []SourcePrintDefinition                   `json:"definitions"`
	References    []SourcePrintReference                    `json:"references"`
	Notices       []string                                  `json:"notices,omitempty"`
}

type SourcePrintIndex struct {
	value  SourcePrintMetadata
	bytes  int64
	merges int
}

func (index *SourcePrintIndex) BudgetBytes() int64 { return index.bytes }

// Snapshot never exposes mutable maps, slices or pointers from the shared LRU.
func (index *SourcePrintIndex) Snapshot() SourcePrintMetadata {
	out := index.value
	out.Properties = maps.Clone(out.Properties)
	for key, fields := range out.Properties {
		out.Properties[key] = maps.Clone(fields)
	}
	out.Names = slices.Clone(out.Names)
	for at := range out.Names {
		out.Names[at].Areas = slices.Clone(out.Names[at].Areas)
	}
	cloneBreaks := func(input []SourcePrintBreak) []SourcePrintBreak {
		values := slices.Clone(input)
		for at := range values {
			values[at].Attributes = maps.Clone(values[at].Attributes)
			if values[at].Manual != nil {
				value := *values[at].Manual
				values[at].Manual = &value
			}
			if values[at].Pivot != nil {
				value := *values[at].Pivot
				values[at].Pivot = &value
			}
		}
		return values
	}
	out.RowBreaks, out.ColBreaks = cloneBreaks(out.RowBreaks), cloneBreaks(out.ColBreaks)
	out.Definitions = slices.Clone(out.Definitions)
	for at := range out.Definitions {
		out.Definitions[at].Attributes = maps.Clone(out.Definitions[at].Attributes)
		out.Definitions[at].Continuation = slices.Clone(out.Definitions[at].Continuation)
	}
	out.References, out.Notices = slices.Clone(out.References), slices.Clone(out.Notices)
	return out
}

type printMetadataBuilder struct {
	value  SourcePrintMetadata
	bytes  int
	seen   map[string]bool
	merges int
}

func newPrintMetadataBuilder(sheet, format string) *printMetadataBuilder {
	return &printMetadataBuilder{value: SourcePrintMetadata{Sheet: sheet, Format: format, Complete: true, NamesComplete: true, Properties: make(map[string]map[string]SourcePrintProperty), Names: []SourcePrintName{}, RowBreaks: []SourcePrintBreak{}, ColBreaks: []SourcePrintBreak{}, Definitions: []SourcePrintDefinition{}, References: []SourcePrintReference{}}, seen: make(map[string]bool)}
}

func (b *printMetadataBuilder) notice(message string) {
	b.value.Complete = false
	if !slices.Contains(b.value.Notices, message) && len(b.value.Notices) < formats.PrintNoticeCount {
		b.value.Notices = append(b.value.Notices, message)
	}
}

func (b *printMetadataBuilder) reserve(value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > formats.PrintMetadataBytes-b.bytes {
		return errors.New("打印元数据超过读取容量，未发布部分设置")
	}
	b.bytes += len(data)
	return nil
}

func (b *printMetadataBuilder) addDefinition(value SourcePrintDefinition) error {
	if len(b.value.Definitions) >= formats.PrintDefinitions {
		return errors.New("打印定义数量超过读取容量")
	}
	if err := b.reserve(value); err != nil {
		return err
	}
	b.value.Definitions = append(b.value.Definitions, value)
	return nil
}

func (b *printMetadataBuilder) unique(kind string) error {
	if b.seen[kind] {
		return errors.New("源工作表打印设置重复，不能确定唯一生效定义：" + kind)
	}
	b.seen[kind] = true
	return nil
}

func (b *printMetadataBuilder) property(group, name, kind, raw string) {
	value := SourcePrintProperty{Type: kind, Value: raw, Status: "stored"}
	trimmed := strings.TrimSpace(raw)
	valid := true
	switch {
	case kind == "bool":
		var flag bool
		flag, valid = sourceBool([]byte(raw))
		if valid {
			value.Value = strconv.FormatBool(flag)
		}
	case kind == "uint":
		_, err := strconv.ParseUint(trimmed, 10, 32)
		valid = err == nil && trimmed != ""
	case kind == "int":
		_, err := strconv.ParseInt(trimmed, 10, 32)
		valid = err == nil && trimmed != ""
	case kind == "decimal":
		number, err := strconv.ParseFloat(trimmed, 64)
		valid = err == nil && number >= 0 && !math.IsInf(number, 0) && !math.IsNaN(number)
	case kind == "measure":
		text := trimmed
		unit := false
		for _, suffix := range []string{"mm", "cm", "in", "pt", "pc", "pi"} {
			if strings.HasSuffix(text, suffix) {
				text, unit = strings.TrimSuffix(text, suffix), true
				break
			}
		}
		number, err := strconv.ParseFloat(text, 64)
		valid = err == nil && number > 0 && !math.IsInf(number, 0) && !math.IsNaN(number)
		if !unit && valid {
			value.Status, value.Reason = "unresolved", "自定义纸张尺寸未含单位，须结合源paperUnits确认"
		}
	case strings.HasPrefix(kind, "enum:"):
		valid = slices.Contains(strings.Split(strings.TrimPrefix(kind, "enum:"), "|"), trimmed)
		value.Type = "enum"
	case kind == "text":
		valid = len(raw) <= formats.PrintTextBytes
	default:
		value.Status, value.Reason = "unresolved", "尚未解释的源打印属性，原值保留"
	}
	if !valid {
		value.Status, value.Reason = "invalid", "源打印属性值无效，未猜测默认值"
	}
	if value.Status != "stored" {
		b.notice(group + "." + name + "：" + value.Reason)
	}
	if b.value.Properties[group] == nil {
		b.value.Properties[group] = make(map[string]SourcePrintProperty)
	}
	b.value.Properties[group][name] = value
}

func (b *printMetadataBuilder) unknown(group, name, kind, reason string) {
	if b.value.Properties[group] == nil {
		b.value.Properties[group] = make(map[string]SourcePrintProperty)
	}
	b.value.Properties[group][name] = SourcePrintProperty{Type: kind, Status: "unresolved", Reason: reason}
	b.notice(reason)
}

func (b *printMetadataBuilder) invalid(group, name, reason string) {
	value := b.value.Properties[group][name]
	value.Status, value.Reason = "invalid", reason
	b.value.Properties[group][name] = value
	b.notice(reason)
}

func (b *printMetadataBuilder) finish() (*SourcePrintIndex, error) {
	data, err := json.Marshal(b.value)
	if err != nil || len(data) > formats.PrintMetadataBytes {
		return nil, errors.New("打印元数据超过读取容量，未发布部分设置")
	}
	return &SourcePrintIndex{value: b.value, merges: b.merges, bytes: int64(len(data)*3 + len(b.value.RowBreaks)*256 + len(b.value.ColBreaks)*256 + len(b.value.Definitions)*512 + 1024)}, nil
}
