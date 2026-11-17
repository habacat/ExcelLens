package reconcile

import "strings"

func excerpt(text string) (string, bool) {
	count := 0
	for at := range text {
		if count == SampleChars {
			return strings.Clone(text[:at]), true
		}
		count++
	}
	return strings.Clone(text), false
}

func sampleGroup(group Group) (Group, int) {
	copy := group
	copy.Key = make([]string, len(group.Key))
	copy.Comparisons = append([]Comparison(nil), group.Comparisons...)
	copy.LeftRows = append([]int(nil), group.LeftRows...)
	copy.RightRows = append([]int(nil), group.RightRows...)
	bytes := 1024
	for _, comparison := range copy.Comparisons {
		bytes += (len(comparison.Delta)+len(comparison.Reason))*2 + 128
	}
	for i, key := range group.Key {
		var clipped bool
		copy.Key[i], clipped = excerpt(key)
		copy.TextTruncated = copy.TextTruncated || clipped
		bytes += len(copy.Key[i])*2 + 32
	}
	clone := func(record *Record) *Record {
		if record == nil {
			return nil
		}
		out := *record
		out.Values = make(map[int]Value, len(record.Values))
		for column, value := range record.Values {
			var rawClipped, displayClipped bool
			value.Raw, rawClipped = excerpt(value.Raw)
			value.Display, displayClipped = excerpt(value.Display)
			value.Truncated = value.Truncated || rawClipped || displayClipped
			copy.TextTruncated = copy.TextTruncated || value.Truncated
			out.Values[column] = value
			bytes += (len(value.Raw)+len(value.Display))*2 + 256
		}
		return &out
	}
	copy.Left, copy.Right = clone(group.Left), clone(group.Right)
	return copy, bytes
}
