package workbook

import (
	"os"
	"path/filepath"

	"excellens/internal/formats"
)

// DataRoot holds local indexes, uploads, exports and historical local records.
// Source workbooks opened from a local path are never moved or modified.
func DataRoot() string {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "ExcelLens")
}

// CacheRoot stores shareable OOXML sheet indexes, search results, and views.
func CacheRoot() string { return filepath.Join(DataRoot(), "cache") }

// ConditionalStatsRoot stores rebuildable cross-workbook conditional statistics caches.
func ConditionalStatsRoot() string {
	return filepath.Join(DataRoot(), formats.ConditionalStatsDirectory)
}

// ConditionalStatsFilePrefix identifies conditional statistics files belonging to one workbook.
func ConditionalStatsFilePrefix(fingerprint string) string { return fingerprint + "-v1-" }

// UploadRoot stores copies of browser-uploaded workbooks until manually cleaned.
func UploadRoot() string { return filepath.Join(DataRoot(), "temp") }
