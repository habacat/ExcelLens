package analysis

import (
	"errors"
	"slices"
	"sort"
	"unicode/utf8"

	"excellens/internal/formats"
)

type TextMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func CloneTransform(step Transform) Transform {
	step.Mapping = slices.Clone(step.Mapping)
	return step
}

func HasTextMappings(q Query) bool {
	for _, step := range q.Transforms {
		if step.Op == "map_text" || len(step.Mapping) != 0 {
			return true
		}
	}
	return false
}

// Sorted exact mappings allow immutable O(log M) lookup per record. Entries
// never cascade: an output matching another input is not mapped again.
func ValidateTextMapping(step Transform) error {
	if step.Op != "map_text" {
		if len(step.Mapping) != 0 {
			return errors.New("文本映射不能附带在其他转换中被忽略")
		}
		return nil
	}
	if step.Arg != "" || len(step.Mapping) == 0 || len(step.Mapping) > formats.TextMappingEntries {
		return errors.New("文本映射参数或数量无效")
	}
	bytes := 0
	for i, entry := range step.Mapping {
		if entry.From == "" || !utf8.ValidString(entry.From) || !utf8.ValidString(entry.To) ||
			utf8.RuneCountInString(entry.From) > formats.TextMappingValueRunes || utf8.RuneCountInString(entry.To) > formats.TextMappingValueRunes ||
			(i > 0 && step.Mapping[i-1].From >= entry.From) {
			return errors.New("文本映射须按原文排序、无重复且每项不超过4000字符")
		}
		bytes += len(entry.From) + len(entry.To) + 64
		if bytes > formats.TextMappingBytes {
			return errors.New("文本映射超过1MiB文字预算")
		}
	}
	return nil
}

func mappedText(text string, entries []TextMapping) (string, bool) {
	at := sort.Search(len(entries), func(i int) bool { return entries[i].From >= text })
	if at < len(entries) && entries[at].From == text {
		return entries[at].To, true
	}
	return text, false
}
