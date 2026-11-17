package ui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"path"
	"strconv"
	"strings"

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
		Clipboard       formats.ClipboardLimits        `json:"clipboard"`
		Printing        formats.PrintingLimits         `json:"printing"`
		Record          formats.RecordCardLimits       `json:"record"`
		CellExplanation formats.CellExplanationLimits  `json:"cellExplanation"`
		Profile         formats.ProfileLimits          `json:"profile"`
		Clustering      formats.ClusteringLimits       `json:"clustering"`
		Composition     formats.CompositionLimits      `json:"composition"`
		SelectionQuick  formats.SelectionQuickLimits   `json:"selectionQuick"`
		ChartExport     formats.ChartExportLimits      `json:"chartExport"`
		Transpose       formats.TransposeLimits        `json:"transpose"`
		Snapshots       formats.SnapshotLimits         `json:"snapshots"`
		RelationExports []formats.RelationExportOption `json:"relationExports"`
		TaskRecovery    struct {
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
		Clipboard:       formats.Clipboard(),
		Printing:        formats.Printing(),
		Record:          formats.RecordCard(),
		CellExplanation: formats.CellExplanation(),
		Profile:         formats.Profile(),
		Clustering:      formats.Clustering(),
		Composition:     formats.Composition(),
		SelectionQuick:  formats.SelectionQuick(),
		ChartExport:     formats.ChartExport(),
		Transpose:       formats.Transpose(),
		Snapshots:       formats.Snapshots(),
		RelationExports: formats.RelationExports(),
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
		"__APP_VERSION__", buildinfo.Version,
		"__MAX_SOURCE_ROWS__", strconv.Itoa(formats.ExternalRows),
		"__FILE_ACCEPT__", formats.Accept(), "__FILE_FORMATS__", string(config))
	return []byte(replacer.Replace(string(data))), nil
}

// Asset names come from embedded production files rather than a second manual
// filename list in the HTTP layer. Test scripts and arbitrary paths are denied.
func ReadAsset(name string) ([]byte, string, error) {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, "", fs.ErrNotExist
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
		return nil, "", fs.ErrNotExist
	}
	data, err := Files.ReadFile("web/" + name)
	return data, contentType, err
}
