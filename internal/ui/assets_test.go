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
	for _, name := range []string{"app.js", "storage-recovery.js", "style.css", "icon.svg", "home.css", "home-texture.svg", "home-opening-texture.svg"} {
		data, mime, etag, err := ReadAssetWithETag(name)
		if err != nil || len(data) == 0 || mime == "" || etag == "" {
			t.Fatal(name, mime, etag, err)
		}
	}
	for _, name := range []string{"", "../web/dist/app.js", "web/dist/app.js", "dist/app.js", `web\dist\app.js`, "index.html", "calculator.test.cjs", "cellview.js"} {
		if data, _, err := ReadAsset(name); err == nil || len(data) > 0 {
			t.Fatal("unsafe or removed path served", name)
		}
	}
	entries, err := Files.ReadDir("web")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".cjs") {
			t.Fatalf("legacy script file remained in embedded web root: %s", name)
		}
	}
	distEntries, err := Files.ReadDir("web/dist")
	if err != nil {
		t.Fatal(err)
	}
	if len(distEntries) != 2 {
		t.Fatalf("expected exactly 2 compiled bundles in web/dist, got %d", len(distEntries))
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

func TestIndexLoadsSingleVersionedBundleAfterDialogs(t *testing.T) {
	pageBytes, err := Index("token")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageBytes)
	scripts := regexp.MustCompile(`<script src="/assets/([^"]+)">`).FindAllStringSubmatch(page, -1)
	if len(scripts) != 1 {
		t.Fatalf("expected exactly 1 asset script tag, got %d: %v", len(scripts), scripts)
	}
	if !regexp.MustCompile(`^app\.js\?v=[a-f0-9]{64}$`).MatchString(scripts[0][1]) {
		t.Fatalf("unexpected bundle script src: %s", scripts[0][1])
	}
	firstScript := strings.Index(page, `<script src="/assets/`)
	lastDialog := strings.LastIndex(page, `<dialog `)
	if firstScript < 0 || lastDialog >= firstScript {
		t.Fatalf("all <dialog> elements must precede asset <script> tags (lastDialog=%d, firstScript=%d)", lastDialog, firstScript)
	}
}

func TestRecoveryIndexLoadsSingleVersionedBundle(t *testing.T) {
	pageBytes, err := RecoveryIndex("recovery-token")
	if err != nil {
		t.Fatal(err)
	}
	page := string(pageBytes)
	if !strings.Contains(page, `content="recovery-token"`) {
		t.Fatal("recovery token was not injected")
	}
	scripts := regexp.MustCompile(`<script src="/assets/([^"]+)">`).FindAllStringSubmatch(page, -1)
	if len(scripts) != 1 {
		t.Fatalf("expected exactly 1 recovery asset script tag, got %d: %v", len(scripts), scripts)
	}
	if !regexp.MustCompile(`^storage-recovery\.js\?v=[a-f0-9]{64}$`).MatchString(scripts[0][1]) {
		t.Fatalf("unexpected recovery bundle script src: %s", scripts[0][1])
	}
}
