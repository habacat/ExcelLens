package ui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"sync"

	"excellens/internal/buildinfo"
	"excellens/internal/formats"
)

// Files embeds the self-contained local browser interface.
//
//go:embed web/*.html web/*.js web/*.css web/*.svg
var Files embed.FS

// Index injects the same registry used by native entry points. JSON is emitted
// with HTML escaping, and the token is generated internally as hexadecimal.
func Index(token string) ([]byte, error) {
	return IndexWithLayout(token, false, 0)
}

// RecoveryIndex has no workbook bootstrap or tab lease.
func RecoveryIndex(token string) ([]byte, error) {
	data, err := Files.ReadFile("web/storage-recovery.html")
	if err != nil {
		return nil, err
	}
	_, urls := assetCatalog()
	return []byte(strings.ReplaceAll(urls.Replace(string(data)), "__APP_TOKEN__", token)), nil
}

func IndexWithLayout(token string, ribbon bool, cacheEpoch uint64) ([]byte, error) {
	data, err := Files.ReadFile("web/index.html")
	if err != nil {
		return nil, err
	}
	config, err := json.Marshal(struct {
		Extensions     []string                  `json:"extensions"`
		Sources        map[string]formats.Source `json:"sources"`
		LargeDataRows  int                       `json:"largeDataRows"`
		QueryTempBytes int64                     `json:"queryTempBytes"`
		RichText       struct {
			CellRuns int `json:"cellRuns"`
			ViewRuns int `json:"viewRuns"`
		} `json:"richText"`
		Clipboard            formats.ClipboardLimits        `json:"clipboard"`
		ReconcileRecordBytes int                            `json:"reconcileRecordBytes"`
		Printing             formats.PrintingLimits         `json:"printing"`
		Record               formats.RecordCardLimits       `json:"record"`
		CellExplanation      formats.CellExplanationLimits  `json:"cellExplanation"`
		Profile              formats.ProfileLimits          `json:"profile"`
		Clustering           formats.ClusteringLimits       `json:"clustering"`
		Composition          formats.CompositionLimits      `json:"composition"`
		SelectionQuick       formats.SelectionQuickLimits   `json:"selectionQuick"`
		ChartExport          formats.ChartExportLimits      `json:"chartExport"`
		Transpose            formats.TransposeLimits        `json:"transpose"`
		Snapshots            formats.SnapshotLimits         `json:"snapshots"`
		Editing              formats.EditLimits             `json:"editing"`
		RelationExports      []formats.RelationExportOption `json:"relationExports"`
		TaskRecovery         struct {
			Tasks int `json:"tasks"`
		} `json:"taskRecovery"`
		Catalogue struct {
			PageRows    int `json:"pageRows"`
			SearchBytes int `json:"searchBytes"`
		} `json:"catalogue"`
		DataRules struct {
			PageRows int `json:"pageRows"`
		} `json:"dataRules"`
		AnalysisTree struct {
			Nodes    int `json:"nodes"`
			Bytes    int `json:"bytes"`
			PageRows int `json:"pageRows"`
			Active   int `json:"active"`
		} `json:"analysisTree"`
	}{Extensions: formats.Extensions(), Sources: formats.Sources(), LargeDataRows: formats.LargeDataRows, QueryTempBytes: formats.MaxQueryTempBytes,
		RichText: struct {
			CellRuns int `json:"cellRuns"`
			ViewRuns int `json:"viewRuns"`
		}{formats.MaxCellRichRuns, formats.RichViewRuns},
		Clipboard:            formats.Clipboard(),
		ReconcileRecordBytes: formats.ReconcileRecordBytes,
		Printing:             formats.Printing(),
		Record:               formats.RecordCard(),
		CellExplanation:      formats.CellExplanation(),
		Profile:              formats.Profile(),
		Clustering:           formats.Clustering(),
		Composition:          formats.Composition(),
		SelectionQuick:       formats.SelectionQuick(),
		ChartExport:          formats.ChartExport(),
		Transpose:            formats.Transpose(),
		Snapshots:            formats.Snapshots(),
		Editing:              formats.Editing(),
		RelationExports:      formats.RelationExports(),
		TaskRecovery: struct {
			Tasks int `json:"tasks"`
		}{formats.CheckpointTasks},
		Catalogue: struct {
			PageRows    int `json:"pageRows"`
			SearchBytes int `json:"searchBytes"`
		}{formats.CataloguePageRows, formats.CatalogueSearchBytes},
		DataRules: struct {
			PageRows int `json:"pageRows"`
		}{formats.DataRulePageRows},
		AnalysisTree: struct {
			Nodes    int `json:"nodes"`
			Bytes    int `json:"bytes"`
			PageRows int `json:"pageRows"`
			Active   int `json:"active"`
		}{formats.AnalysisTreeBrowserNodes, formats.AnalysisTreeBrowserBytes, formats.AnalysisTreePageRows, formats.AnalysisTreeActiveTasks}})
	if err != nil {
		return nil, err
	}
	replacer := strings.NewReplacer("__APP_TOKEN__", token,
		"__RIBBON_LAYOUT__", strconv.FormatBool(ribbon), "__CACHE_EPOCH__", strconv.FormatUint(cacheEpoch, 10),
		"__APP_VERSION__", buildinfo.Version,
		"__MAX_SOURCE_ROWS__", strconv.Itoa(formats.ExternalRows),
		"__FILE_ACCEPT__", formats.Accept(), "__FILE_FORMATS__", string(config))
	_, urls := assetCatalog()
	return []byte(replacer.Replace(urls.Replace(string(data)))), nil
}

// The index names the exact bytes it uses. Unversioned URLs (including CSS
// image references and local clients) revalidate on each use.
var assetCatalog = sync.OnceValues(func() (map[string]string, *strings.Replacer) {
	entries, _ := Files.ReadDir("web")
	tags := make(map[string]string, len(entries))
	var urls []string
	for _, entry := range entries {
		name := entry.Name()
		switch path.Ext(name) {
		case ".js", ".css", ".svg":
			data, err := Files.ReadFile("web/" + name)
			if err != nil {
				continue
			}
			digest := sha256.Sum256(data)
			hash := hex.EncodeToString(digest[:])
			tags[name] = `"` + hash + `"`
			url := "/assets/" + name
			urls = append(urls, url+`"`, url+"?v="+hash+`"`)
		}
	}
	return tags, strings.NewReplacer(urls...)
})

// Asset names come from embedded production files rather than a second manual
// filename list in the HTTP layer. Test scripts and arbitrary paths are denied.
func ReadAsset(name string) ([]byte, string, error) {
	data, contentType, _, err := ReadAssetWithETag(name)
	return data, contentType, err
}

// ReadAssetWithETag returns the embedded asset bytes, MIME type, and a strong
// ETag derived from the asset's SHA-256 digest.
func ReadAssetWithETag(name string) ([]byte, string, string, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, "", "", fs.ErrNotExist
	}
	var contentType string
	switch path.Ext(name) {
	case ".js":
		contentType = "text/javascript; charset=utf-8"
	case ".css":
		contentType = "text/css; charset=utf-8"
	case ".svg":
		contentType = "image/svg+xml"
	default:
		return nil, "", "", fs.ErrNotExist
	}
	data, err := Files.ReadFile("web/" + name)
	if err != nil {
		return nil, "", "", err
	}
	tags, _ := assetCatalog()
	return data, contentType, tags[name], nil
}
