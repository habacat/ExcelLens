package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"

	"excellens/internal/analysis"
	"excellens/internal/reconcile"
	"excellens/internal/sourceview"
)

// Existing content identity, shared without changing its inputs or encoding.
func analysisSheetStamp(sheet *analysis.Sheet) string {
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	_ = encoder.Encode(sheet.Columns)
	_ = encoder.Encode(sheet.Revision)
	for _, row := range sheet.Rows {
		_ = encoder.Encode(row)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func sourceHeaderIdentities(info reconcile.SourceInfo) []sourceview.HeaderIdentity {
	counts := make(map[string]int, len(info.Fields))
	for _, field := range info.Fields {
		if field.HeaderLoaded && field.RawName != "" {
			counts[field.HeaderHash]++
		}
	}
	complete := info.Kind == "analysis" || len(info.Fields) <= maxSourceHeaderColumns
	out := make([]sourceview.HeaderIdentity, len(info.Fields))
	for i, field := range info.Fields {
		out[i] = sourceview.HeaderIdentity{ID: "C" + strconv.Itoa(field.Column), Hash: field.HeaderHash, Complete: field.HeaderLoaded && field.RawName != "" && !field.Truncated, Unique: complete && counts[field.HeaderHash] == 1}
	}
	return out
}
