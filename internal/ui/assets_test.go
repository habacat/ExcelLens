package ui

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func TestEmbeddedProductionAssetsAndDeniedPaths(t *testing.T) {
	for _, name := range []string{"cellview.js", "style.css", "icon.svg", "home.css", "home-texture.svg", "home-opening-texture.svg"} {
		data, mime, err := ReadAsset(name)
		if err != nil || len(data) == 0 || mime == "" {
			t.Fatal(name, mime, err)
		}
	}
	for _, name := range []string{"", "../web/app.js", "web/app.js", `web\app.js`, "index.html", "calculator.test.cjs"} {
		if data, _, err := ReadAsset(name); err == nil || len(data) > 0 {
			t.Fatal("unsafe path served", name)
		}
	}
	entries, err := Files.ReadDir("web")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".test.cjs") {
			t.Fatal("test scripts included in runtime assets")
		}
	}
}

func TestIndexUsesNativeFormatRegistry(t *testing.T) {
	page, err := Index("test-token")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), "__FILE_") || !strings.Contains(string(page), `accept="`+formats.Accept()+`"`) {
		t.Fatal("file picker bootstrap was not resolved")
	}
	if strings.Contains(string(page), "__MAX_SOURCE_ROWS__") || !strings.Contains(string(page), `max="1000000000"`) {
		t.Fatal("row limits not injected")
	}
	match := regexp.MustCompile(`<script id="fileFormats" type="application/json">([^<]+)</script>`).FindSubmatch(page)
	if len(match) != 2 {
		t.Fatal("missing safe format configuration")
	}
	var config struct {
		Extensions     []string                     `json:"extensions"`
		Record         formats.RecordCardLimits     `json:"record"`
		SelectionQuick formats.SelectionQuickLimits `json:"selectionQuick"`
		ChartExport    formats.ChartExportLimits    `json:"chartExport"`
		Composition    formats.CompositionLimits    `json:"composition"`
		Sources        map[string]formats.Source    `json:"sources"`
		RichText       struct {
			CellRuns int `json:"cellRuns"`
			ViewRuns int `json:"viewRuns"`
		} `json:"richText"`
		Clipboard struct {
			TextBytes int `json:"textBytes"`
			HTMLBytes int `json:"htmlBytes"`
		} `json:"clipboard"`
	}
	if err := json.Unmarshal(match[1], &config); err != nil {
		t.Fatal(err)
	}
	if strings.Join(config.Extensions, ",") != formats.Accept() || config.Sources["xls"].TextAmounts || config.Sources["xls-html"].MaxRows != formats.ExternalRows {
		t.Fatal("browser and reader format semantics differ")
	}
	if config.RichText.CellRuns != formats.MaxCellRichRuns || config.RichText.ViewRuns != formats.RichViewRuns {
		t.Fatal("browser and reader presentation capacities differ")
	}
	if config.Record != formats.RecordCard() {
		t.Fatal("browser and server record capacities differ")
	}
	if config.Composition != formats.Composition() {
		t.Fatal("browser and server composition capacities differ")
	}
	if config.SelectionQuick != formats.SelectionQuick() {
		t.Fatal("browser and server quick selection capacities differ")
	}
	if !reflect.DeepEqual(config.ChartExport, formats.ChartExport()) {
		t.Fatal("browser and image export capacities differ")
	}
	if config.Clipboard.TextBytes != formats.ClipboardTextBytes || config.Clipboard.HTMLBytes != formats.ClipboardHTMLBytes {
		t.Fatal("browser and server clipboard capacities differ")
	}
}

func TestQuickSelectionControllerLoadsBeforeAppBootstrap(t *testing.T) {
	page, err := Index("token")
	if err != nil {
		t.Fatal(err)
	}
	model := strings.Index(string(page), `src="/assets/selection-quick-model.js"`)
	controller := strings.Index(string(page), `src="/assets/selection-quick.js"`)
	app := strings.Index(string(page), `src="/assets/app.js"`)
	if model < 0 || controller <= model || app <= controller {
		t.Fatal("quick selection constructor must exist when app initializes")
	}
}

func TestChartExportModulesLoadBeforeAnalytics(t *testing.T) {
	page, err := Index("token")
	if err != nil {
		t.Fatal(err)
	}
	previous := strings.Index(string(page), `src="/assets/analysis-results.js"`)
	for _, name := range []string{"chart-export-model.js", "chart-export-surface.js", "chart-export.js", "analytics.js"} {
		at := strings.Index(string(page), `src="/assets/`+name+`"`)
		if previous < 0 || at <= previous {
			t.Fatal("image export module load order", name)
		}
		if _, _, err := ReadAsset(name); err != nil {
			t.Fatal(name, err)
		}
		previous = at
	}
}
