package workbook

import (
	"os"
	"path/filepath"
)

// DataRoot is the only directory containing ExcelLens-owned persistent data.
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

// UploadRoot stores copies of browser-uploaded workbooks until manually cleaned.
func UploadRoot() string { return filepath.Join(DataRoot(), "temp") }
