package reconcile

import (
	"encoding/json"
	"strings"

	"excellens/internal/formats"
)

type relationCSVCellState struct {
	kind, state string
	flags       byte
}

func relationCSVState(v Value) relationCSVCellState {
	var flags byte
	if v.Exists {
		flags |= 1
	}
	if v.MissingCache {
		flags |= 2
	}
	if v.TypeMissing {
		flags |= 4
	}
	if v.Truncated {
		flags |= 8
	}
	return relationCSVCellState{v.Type, v.Uncertain, flags}
}

type relationCSVStateEncoding struct {
	cells   []relationCSVCellState
	encoded [6]string
}

type relationCSVStateCache struct {
	entries map[uint64]*relationCSVStateEncoding
	bytes   int
}

// The signature is only an index. Full ordered state equality is required on
// a hit; a collision bypasses admission and cannot substitute another state.
func relationCSVStateHash(record *Record, columns []int) uint64 {
	h := uint64(14695981039346656037)
	text := func(s string) {
		h = (h ^ uint64(len(s))) * 1099511628211
		for i := 0; i < len(s); i++ {
			h = (h ^ uint64(s[i])) * 1099511628211
		}
	}
	for _, column := range columns {
		v := record.Values[column]
		text(v.Type)
		text(v.Uncertain)
		h = (h ^ uint64(relationCSVState(v).flags)) * 1099511628211
	}
	return h
}

func (entry *relationCSVStateEncoding) matches(record *Record, columns []int) bool {
	if len(entry.cells) != len(columns) {
		return false
	}
	for i, column := range columns {
		if entry.cells[i] != relationCSVState(record.Values[column]) {
			return false
		}
	}
	return true
}

func (cache *relationCSVStateCache) encode(record *Record, columns []int) [6]string {
	signature := relationCSVStateHash(record, columns)
	if entry := cache.entries[signature]; entry != nil && entry.matches(record, columns) {
		return entry.encoded
	}
	kinds, states := make([]string, len(columns)), make([]string, len(columns))
	exists, missing, typeMissing, truncated := make([]bool, len(columns)), make([]bool, len(columns)), make([]bool, len(columns)), make([]bool, len(columns))
	retained := 256 + len(columns)*64
	for i, column := range columns {
		v := record.Values[column]
		kinds[i], states[i], exists[i], missing[i], typeMissing[i], truncated[i] = v.Type, v.Uncertain, v.Exists, v.MissingCache, v.TypeMissing, v.Truncated
		retained += len(v.Type) + len(v.Uncertain)
	}
	var encoded [6]string
	for i, value := range []any{kinds, exists, missing, typeMissing, truncated, states} {
		data, _ := json.Marshal(value)
		encoded[i] = string(data)
		retained += len(data)
	}
	if len(cache.entries) < formats.RelationCSVCacheEntries && retained <= formats.RelationCSVCacheBytes-cache.bytes && cache.entries[signature] == nil {
		if cache.entries == nil {
			cache.entries = make(map[uint64]*relationCSVStateEncoding)
		}
		entry := &relationCSVStateEncoding{cells: make([]relationCSVCellState, len(columns)), encoded: encoded}
		for i, column := range columns {
			v := relationCSVState(record.Values[column])
			// Bound retained backing storage as well as visible string lengths.
			v.kind, v.state = strings.Clone(v.kind), strings.Clone(v.state)
			entry.cells[i] = v
		}
		cache.entries[signature] = entry
		cache.bytes += retained
	}
	return encoded
}
