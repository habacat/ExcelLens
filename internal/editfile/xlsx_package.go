package editfile

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

const (
	mainNS        = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	contentNS     = "http://schemas.openxmlformats.org/package/2006/content-types"
	packageNS     = "http://schemas.openxmlformats.org/package/2006/relationships"
	relationNS    = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	markupNS      = "http://schemas.openxmlformats.org/markup-compatibility/2006"
	coreNS        = "http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
	appNS         = "http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"
	themeNS       = "http://schemas.openxmlformats.org/drawingml/2006/main"
	spreadMIME    = "application/vnd.openxmlformats-officedocument.spreadsheetml."
	vmlMIME       = "application/vnd.openxmlformats-officedocument.vmlDrawing"
	printerMIME   = spreadMIME + "printerSettings"
	calcChainPart = "xl/calcChain.xml"
	macroBookMIME = "application/vnd.ms-excel.sheet.macroEnabled.main+xml"
)

var ErrXLSX = errors.New("此 XLSX 含尚不支持安全编辑的结构或部件")

type XLSXSheet struct {
	Name         string `json:"name"`
	Part         string `json:"part"`
	FirstRow     int    `json:"firstRow"`
	FirstCol     int    `json:"firstCol"`
	Rows         int    `json:"rows"`
	Cols         int    `json:"cols"`
	Formulas     int    `json:"formulas,omitempty"`
	Cached       int    `json:"cached,omitempty"`
	SharedCached int    `json:"sharedCached,omitempty"`
	Rules        int    `json:"rules,omitempty"`
	ReadOnly     string `json:"readOnly,omitempty"`
}

// GeneratedArea describes cached results owned by an array anchor or a pivot.
type GeneratedArea struct {
	editdoc.Area
	Input string `json:"input"`
	Pivot bool   `json:"pivot,omitempty"`
}

type XLSXLayout struct {
	ChartSheets    []string            `json:"chartSheets,omitempty"`
	Pivots         []PivotInfo         `json:"pivots,omitempty"`
	PivotAxesReady bool                `json:"pivotAxesReady,omitempty"`
	Tables         []xlsxTable         `json:"tables,omitempty"`
	Inputs         map[string][]string `json:"inputs,omitempty"`
	Generated      []GeneratedArea     `json:"generated,omitempty"`
	Size           int64               `json:"size"`
	Sheets         []XLSXSheet         `json:"sheets"`
	Rules          []editdoc.Rule      `json:"rules,omitempty"`
	GridEditing    bool                `json:"gridEditing,omitempty"`
	Structured     bool                `json:"structured,omitempty"`
	Native         bool                `json:"native,omitempty"`
	MacroEnabled   bool                `json:"macroEnabled,omitempty"`
	RecalcOnOpen   bool                `json:"recalcOnOpen,omitempty"`
}

func (l XLSXLayout) Valid() bool {
	if !l.validTables() || l.Size <= 0 || l.Size > formats.EditOutputBytes || len(l.Sheets) == 0 || len(l.Sheets) > formats.EditOfficeParts || len(l.Rules) > formats.EditValidationRules || l.Native && !l.Structured {
		return false
	}
	names, parts := map[string]bool{}, map[string]bool{}
	for _, s := range l.Sheets {
		name := strings.ToLower(s.Name)
		if name == "" || len(name) > 512 || names[name] || parts[s.Part] || !sheetPart.MatchString(s.Part) || s.Rows < 0 || s.Rows > formats.ExcelRows || s.Cols < 0 || s.Cols > formats.ExcelColumns || s.FirstRow < 0 || s.FirstRow > s.Rows || s.FirstCol < 0 || s.FirstCol > s.Cols ||
			s.Formulas < 0 || s.Formulas > formats.EditXLSXPartBytes || s.Cached < 0 || s.Cached > s.Formulas || s.SharedCached < 0 || s.SharedCached > s.Cached || s.Rules < 0 || s.Rules > formats.EditValidationRules {
			return false
		}
		if s.ReadOnly != "" && (!l.Structured || !wholeSheetLock(s.ReadOnly)) {
			return false
		}
		names[name], parts[s.Part] = true, true
	}
	for sheet, inputs := range l.Inputs {
		if !names[strings.ToLower(sheet)] {
			return false
		}
		for _, input := range inputs {
			if !names[strings.ToLower(input)] {
				return false
			}
		}
	}
	for _, a := range l.Generated {
		if !a.Area.Valid() || !names[strings.ToLower(a.Start.Sheet)] || !names[strings.ToLower(a.Input)] {
			return false
		}
	}
	counts := map[string]int{}
	for i, rule := range l.Rules {
		if !rule.Valid() {
			return false
		}
		for _, prev := range l.Rules[:i] {
			if prev.Intersects(rule.Area) {
				return false
			}
		}
		counts[rule.Start.Sheet]++
	}
	for _, s := range l.Sheets {
		if counts[s.Name] != s.Rules {
			return false
		}
		delete(counts, s.Name)
	}
	if len(counts) != 0 {
		return false
	}
	return true
}

func (l XLSXLayout) HasSheet(name string) bool {
	for _, sheet := range l.Sheets {
		if strings.EqualFold(sheet.Name, name) {
			return true
		}
	}
	return false
}

type xlsxPackage struct {
	zip                *zip.Reader
	files              map[string]*zip.File
	layout             XLSXLayout
	styles             int
	unlockedStyles     map[string]bool
	shared             []string
	sheetIDs           map[string]bool
	fullRecalc         bool
	displayPrecision   bool
	date1904           bool
	calendarErr        error
	staleFormulaCaches bool
	links              map[string]map[string]string
	cacheIDs           map[string]string
	names              map[nameKey]nameTarget
	locks              map[string]string
	pivotSheets        map[string]string
	pivotSources       map[string]pivotSource
	pivotOutputs       map[string]string
	pivotRanges        map[string][]ownedRange
	dynamic            bool
}

func openXLSXPackage(r io.ReaderAt, size int64) (*xlsxPackage, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	if len(z.File) > formats.EditOfficeParts {
		return nil, fmt.Errorf("%w：ZIP 条目数量超限", ErrXLSX)
	}
	p := &xlsxPackage{zip: z, files: map[string]*zip.File{}, layout: XLSXLayout{Size: size}}
	seen := make(map[string]bool, len(z.File))
	var directories []string
	var expanded uint64
	for _, f := range z.File {
		name, directory := strings.CutSuffix(f.Name, "/")
		key := strings.ToLower(name)
		if seen[key] || f.Flags&1 != 0 || (f.Method != zip.Store && f.Method != zip.Deflate) || f.UncompressedSize64 > formats.EditOfficePartBytes {
			return nil, fmt.Errorf("%w：%s", ErrXLSX, f.Name)
		}
		seen[key] = true
		if directory {
			if !emptyXLSXDirectory(f, name) {
				return nil, fmt.Errorf("%w：%s", ErrXLSX, f.Name)
			}
			directories = append(directories, f.Name)
			continue
		}
		kind := classifyPart(name)
		if kind.role == "" || !f.Mode().IsRegular() {
			return nil, fmt.Errorf("%w：%s", ErrXLSX, f.Name)
		}
		if kind.mode != plainPart {
			p.layout.Structured = true
		}
		expanded += f.UncompressedSize64
		if expanded > formats.EditOfficePackageBytes {
			return nil, fmt.Errorf("%w：解压总量超限", ErrXLSX)
		}
		if len(z.File) > formats.EditXLSXParts || expanded > formats.EditXLSXPackageBytes || f.UncompressedSize64 > formats.EditXLSXPartBytes || kind.mode == coordinatePart {
			p.layout.Native, p.layout.Structured = true, true
		}
		p.files[f.Name] = f
	}
	for _, dir := range directories {
		key := strings.ToLower(dir)
		for name := range p.files {
			if strings.HasPrefix(key, strings.ToLower(name)+"/") {
				return nil, fmt.Errorf("%w：%s", ErrXLSX, dir)
			}
		}
	}
	if expanded > formats.EditOfficeLocalScanBytes {
		for name := range p.files {
			if noteDrawingPart.MatchString(name) {
				p.layout.Native = true
				break
			}
		}
	}
	for _, need := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels"} {
		if p.files[need] == nil {
			return nil, fmt.Errorf("%w：缺少 %s", ErrXLSX, need)
		}
	}
	return p, nil
}

// Empty directory records are ZIP metadata. Some producers encode them with
// an empty DEFLATE stream. Check the actual stream before ignoring the entry.
func emptyXLSXDirectory(f *zip.File, name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") || f.Mode().Type() != fs.ModeDir ||
		f.UncompressedSize64 != 0 || f.CRC32 != 0 || f.CompressedSize64 > formats.EditXLSXDirectoryBytes {
		return false
	}
	raw, err := f.OpenRaw()
	if err != nil || f.CompressedSize64 == 0 {
		return err == nil
	}
	if f.Method != zip.Deflate {
		return false
	}
	var compressed [formats.EditXLSXDirectoryBytes]byte
	data := compressed[:int(f.CompressedSize64)]
	if _, err := io.ReadFull(raw, data); err != nil {
		return false
	}
	input := bytes.NewReader(data)
	r := flate.NewReader(input)
	defer r.Close()
	var one [1]byte
	n, err := r.Read(one[:])
	return n == 0 && err == io.EOF && input.Len() == 0
}

func (p *xlsxPackage) readPart(ctx context.Context, name string) ([]byte, error) {
	f := p.files[name]
	if f == nil {
		return nil, ErrXLSX
	}
	if f.UncompressedSize64 > formats.EditXLSXPartBytes {
		return nil, fmt.Errorf("%w：%s 超过单个内存部件的大小上限", ErrXLSX, name)
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data := make([]byte, int(f.UncompressedSize64))
	for pos := 0; pos < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		next := min(pos+(128<<10), len(data))
		if _, err := io.ReadFull(r, data[pos:next]); err != nil {
			return nil, err
		}
		pos = next
	}
	var more [1]byte
	if n, err := r.Read(more[:]); n != 0 || err != io.EOF {
		return nil, ErrXLSX
	}
	return data, nil
}

func (p *xlsxPackage) types(ctx context.Context) error {
	data, err := p.readPart(ctx, "[Content_Types].xml")
	if err != nil {
		return err
	}
	defs, overrides := map[string]string{}, map[string]string{}
	err = walkXML(ctx, data, func(e xmlEvent) error {
		if t, ok := e.token.(xml.CharData); ok && len(bytes.TrimSpace(t)) != 0 {
			return ErrXLSX
		}
		el, ok := e.token.(xml.StartElement)
		if !ok {
			return nil
		}
		if el.Name.Space != contentNS {
			return ErrXLSX
		}
		if e.parent() == "" && e.local() == "Types" {
			return nil
		}
		if len(e.stack) != 2 || e.parent() != "Types" {
			return ErrXLSX
		}
		mime := attribute(el, "", "ContentType")
		if mime == "" {
			return ErrXLSX
		}
		switch e.local() {
		case "Default":
			ext := attribute(el, "", "Extension")
			if defs[ext] != "" || ext != "rels" && ext != "xml" && !(ext == "png" && mime == "image/png") && !((ext == "jpg" || ext == "jpeg") && mime == "image/jpeg") && !(ext == "vml" && mime == vmlMIME) && !(ext == "bin" && (mime == printerMIME || mime == vbaMIME)) {
				return ErrXLSX
			}
			defs[ext] = mime
		case "Override":
			name, absolute := strings.CutPrefix(attribute(el, "", "PartName"), "/")
			if name == "xl/workbook.xml" && mime == macroBookMIME {
				p.layout.MacroEnabled, p.layout.Native, p.layout.Structured = true, true, true
			}
			if !absolute || p.files[name] == nil || overrides[name] != "" || mime != p.partMIME(name) {
				return ErrXLSX
			}
			overrides[name] = mime
		default:
			return ErrXLSX
		}
		return nil
	})
	if err != nil {
		return err
	}
	for name := range p.files {
		kind := classifyPart(name)
		if kind.role == "types" {
			continue
		}
		mime := overrides[name]
		if (name == calcChainPart || kind.mode != plainPart && kind.role != "customXml" && path.Ext(name) == ".xml") && mime == "" {
			return fmt.Errorf("%w：缺少明确的部件类型 %s", ErrXLSX, name)
		}
		if mime == "" {
			mime = defs[strings.TrimPrefix(path.Ext(name), ".")]
		}
		if mime != p.partMIME(name) || kind.role == "vba" && !p.layout.MacroEnabled {
			return fmt.Errorf("%w：部件类型 %s", ErrXLSX, name)
		}
	}
	return nil
}

func (p *xlsxPackage) relationships(ctx context.Context, part, owner string) (map[string]string, error) {
	data, err := p.readPart(ctx, part)
	if err != nil {
		return nil, err
	}
	links, seen := map[string]string{}, map[string]bool{}
	err = walkXML(ctx, data, func(e xmlEvent) error {
		if t, ok := e.token.(xml.CharData); ok && len(bytes.TrimSpace(t)) != 0 {
			return ErrXLSX
		}
		el, ok := e.token.(xml.StartElement)
		if !ok {
			return nil
		}
		if el.Name.Space != packageNS {
			return ErrXLSX
		}
		if e.parent() == "" && e.local() == "Relationships" {
			if !plainAttributes(el) {
				return ErrXLSX
			}
			return nil
		}
		id, target, mode := attribute(el, "", "Id"), attribute(el, "", "Target"), attribute(el, "", "TargetMode")
		if len(e.stack) != 2 || e.parent() != "Relationships" || e.local() != "Relationship" || id == "" || target == "" || links[id] != "" ||
			!plainAttributes(el, "Id", "Type", "Target", "TargetMode") {
			return ErrXLSX
		}
		if mode == "External" && ((externalBookPart.MatchString(owner) && attribute(el, "", "Type") == relationNS+"/externalLinkPath") || (p.layout.Native && sheetPart.MatchString(owner) && attribute(el, "", "Type") == relationNS+"/hyperlink")) {
			// Opaque handle only. External targets are never resolved or opened.
			links[id] = externalBookTarget
			return nil
		}
		if mode != "" && mode != "Internal" {
			return ErrXLSX
		}
		target, ok = packageTarget(owner, target)
		if !ok {
			return ErrXLSX
		}
		kind := classifyPart(target)
		if !allowsRelationship(owner, kind.role) || attribute(el, "", "Type") != kind.relation || p.files[target] == nil || seen[target] {
			return ErrXLSX
		}
		links[id], seen[target] = target, true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return links, nil
}

func (p *xlsxPackage) readWorkbook(ctx context.Context, links map[string]string) error {
	data, err := p.readPart(ctx, "xl/workbook.xml")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	p.sheetIDs = map[string]bool{}
	extensions := workbookExtensions{p: p, links: links, seen: seen}
	err = walkXML(ctx, data, func(e xmlEvent) error {
		if handled, err := extensions.visit(e); handled {
			return err
		}
		if t, ok := e.token.(xml.CharData); ok && len(bytes.TrimSpace(t)) != 0 {
			return ErrXLSX
		}
		el, ok := e.token.(xml.StartElement)
		if !ok {
			return nil
		}
		if el.Name.Space != mainNS {
			return ErrXLSX
		}
		switch e.parent() {
		case "":
			if e.local() == "workbook" && mainAttributes(el) {
				return nil
			}
		case "workbook":
			switch e.local() {
			case "fileVersion", "workbookPr", "bookViews", "sheets", "definedNames", "calcPr":
				if !seen[e.local()] && mainAttributes(el) {
					seen[e.local()] = true
					if e.local() == "workbookPr" {
						p.date1904, p.calendarErr = calendarProperties(el)
					}
					if e.local() == "calcPr" {
						p.fullRecalc = requestsFullRecalc(el)
						value := attribute(el, "", "fullPrecision")
						p.displayPrecision = value == "0" || value == "false"
						completed := attribute(el, "", "calcCompleted")
						p.staleFormulaCaches = isTrue(attribute(el, "", "fullCalcOnLoad")) || isTrue(attribute(el, "", "forceFullCalc")) || completed == "0" || completed == "false"
					}
					return nil
				}
			case "workbookProtection":
				// Some producers emit this empty marker even for unlocked
				// workbooks. A lock, credential or unknown setting is refused.
				for _, attr := range el.Attr {
					if !nsDeclaration(attr) {
						return fmt.Errorf("%w：工作簿保护", ErrXLSX)
					}
				}
				if !seen[e.local()] {
					seen[e.local()] = true
					return nil
				}
			}
		case "bookViews":
			if e.local() == "workbookView" && mainAttributes(el) {
				return nil
			}
		case "sheets":
			name, part := attribute(el, "", "name"), links[attribute(el, relationNS, "id")]
			if e.local() == "sheet" && name != "" && sheetPart.MatchString(part) {
				id := attribute(el, "", "sheetId")
				if !validSheetID(id) || p.sheetIDs[id] {
					return fmt.Errorf("%w：工作表 ID 无效", ErrXLSX)
				}
				for _, a := range el.Attr {
					if !nsDeclaration(a) && a.Name != (xml.Name{Space: relationNS, Local: "id"}) && (a.Name.Space != "" || a.Name.Local != "name" && a.Name.Local != "sheetId" && a.Name.Local != "state") {
						return ErrXLSX
					}
				}
				p.sheetIDs[id] = true
				p.layout.Sheets = append(p.layout.Sheets, XLSXSheet{Name: name, Part: part})
				return nil
			}
		}
		return fmt.Errorf("%w：工作簿元素 %s", ErrXLSX, e.local())
	})
	if err != nil {
		return err
	}
	return extensions.resolveNames(ctx)
}

func (p *xlsxPackage) metadata(ctx context.Context) error {
	for _, file := range p.zip.File {
		role := classifyPart(file.Name).role
		if role != "styles" && role != "shared" && role != "theme" && role != "app" && role != "core" && role != "custom" {
			continue
		}
		data, err := p.readPart(ctx, file.Name)
		if err != nil {
			return err
		}
		if role == "custom" {
			if err := checkXLSXCustomProperties(ctx, data); err != nil {
				return fmt.Errorf("%s：%w", file.Name, err)
			}
			continue
		}
		if role == "styles" {
			if err := p.readStyles(ctx, data); err != nil {
				return fmt.Errorf("%s：%w", file.Name, err)
			}
			continue
		}
		var text strings.Builder
		var hasText, rich, runText bool
		err = walkXML(ctx, data, func(e xmlEvent) error {
			switch t := e.token.(type) {
			case xml.StartElement:
				if role != "theme" && (e.local() == "extLst" || e.local() == "AlternateContent") {
					return ErrXLSX
				}
				switch role {
				case "shared":
					if t.Name.Space != mainNS || !mainAttributes(t) {
						return ErrXLSX
					}
					switch {
					case e.parent() == "" && e.local() == "sst":
					case e.parent() == "sst" && e.local() == "si":
						text.Reset()
						hasText, rich = false, false
					case e.parent() == "si" && e.local() == "t" && !hasText && !rich:
						hasText = true
					case e.parent() == "si" && e.local() == "r" && !hasText:
						p.layout.Structured = true
						rich, runText = true, false
					case e.parent() == "r" && e.local() == "rPr" && !runText:
					case e.parent() == "r" && e.local() == "t" && !runText:
						runText = true
					case e.parent() == "si" && e.local() == "rPh":
					case e.parent() == "rPh" && e.local() == "t":
					case e.parent() == "rPr" && slices.Contains(commentChildren["rPr"], e.local()):
					case e.parent() == "si" && e.local() == "phoneticPr":
						p.layout.Structured = true
					default:
						return fmt.Errorf("%w：共享字符串含富文本或扩展", ErrXLSX)
					}
				case "theme":
					if len(e.stack) > 2 && e.stack[len(e.stack)-2] == (xml.Name{Space: themeNS, Local: "ext"}) && t.Name == (xml.Name{Space: "http://schemas.microsoft.com/office/thememl/2012/main", Local: "themeFamily"}) && plainAttributes(t, "name", "id", "vid") {
						return nil
					}
					if t.Name.Space != themeNS || e.parent() == "" && e.local() != "theme" {
						return ErrXLSX
					}
				case "core":
					if e.parent() == "" && t.Name != (xml.Name{Space: coreNS, Local: "coreProperties"}) {
						return ErrXLSX
					}
				case "app":
					if e.parent() == "" && t.Name != (xml.Name{Space: appNS, Local: "Properties"}) {
						return ErrXLSX
					}
				}
			case xml.CharData:
				if role == "shared" {
					if e.local() == "t" && e.parent() == "rPh" {
						return nil
					}
					if e.local() == "t" {
						text.Write(t)
					} else if len(bytes.TrimSpace(t)) != 0 {
						return ErrXLSX
					}
				}
			case xml.EndElement:
				if role == "shared" && e.local() == "r" && !runText {
					return ErrXLSX
				}
				if role == "shared" && e.local() == "si" {
					p.shared = append(p.shared, text.String())
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("%s：%w", file.Name, err)
		}
	}
	return nil
}

func (p *xlsxPackage) inspect(ctx context.Context) error {
	if err := p.types(ctx); err != nil {
		return fmt.Errorf("[Content_Types].xml：%w", err)
	}
	if err := p.loadWorkbook(ctx); err != nil {
		return err
	}
	if p.layout.Native {
		return p.inspectOfficeParts(ctx)
	}
	if err := p.checkCalcChain(ctx); err != nil {
		return err
	}
	return p.inspectStructuredParts(ctx)
}

// loadWorkbook reads the structure needed to locate cells in the source
// already bound by its digest. Full preflight separately validates payloads.
func (p *xlsxPackage) loadWorkbook(ctx context.Context) error {
	if err := p.readRelationships(ctx); err != nil {
		return err
	}
	links := p.links["xl/workbook.xml"]
	if err := p.readWorkbook(ctx, links); err != nil {
		return fmt.Errorf("xl/workbook.xml：%w", err)
	}
	count := 0
	for _, name := range links {
		if sheetPart.MatchString(name) {
			count++
		}
	}
	if !p.layout.Valid() || count != len(p.layout.Sheets) {
		return ErrXLSX
	}
	if err := p.loadTables(ctx); err != nil {
		return err
	}
	if err := p.metadata(ctx); err != nil {
		return err
	}
	if p.files[sheetMetadataPart] != nil {
		return p.checkDynamicMetadata(ctx)
	}
	return nil
}

func positiveInt(text string, limit int) (int, bool) {
	n, err := strconv.Atoi(text)
	return n, err == nil && n >= 1 && n <= limit && strconv.Itoa(n) == text
}

func InspectXLSX(ctx context.Context, f *workbook.EditSourceFile, wb *workbook.Workbook) (XLSXLayout, string, error) {
	proof, err := f.SHA256(ctx)
	if err != nil {
		return XLSXLayout{}, "", err
	}
	p, err := openXLSXPackage(f, f.Size())
	if err == nil {
		err = p.inspect(ctx)
	}
	if err != nil {
		return XLSXLayout{}, "", err
	}
	if p.layout.MacroEnabled != strings.EqualFold(filepath.Ext(wb.Path), ".xlsm") {
		return XLSXLayout{}, "", fmt.Errorf("%w：工作簿内容类型与扩展名不匹配", ErrXLSX)
	}
	if len(p.layout.Sheets) != len(wb.Sheets) {
		return XLSXLayout{}, "", ErrBinding
	}
	for i, s := range p.layout.Sheets {
		if s.Name != wb.Sheets[i].Name || s.Part != wb.Sheets[i].Path {
			return XLSXLayout{}, "", ErrBinding
		}
	}
	if err = p.inspectGrid(ctx); err != nil {
		return XLSXLayout{}, "", err
	}
	bound := Layout{XLSX: &p.layout}
	refs, err := bound.referenceRules(p.layout.Rules)
	if err != nil {
		return XLSXLayout{}, "", fmt.Errorf("%w：%w", ErrXLSX, err)
	}
	if len(refs) != 0 {
		targets, err := appendReferences(nil, refs)
		if err != nil {
			return XLSXLayout{}, "", fmt.Errorf("%w：%w", ErrXLSX, err)
		}
		values, err := p.readCells(ctx, targets)
		if err != nil {
			return XLSXLayout{}, "", err
		}
		for _, rule := range refs {
			if _, err := rule.referencedChoices(func(a editdoc.Address) editdoc.Value { return values[a].Value }); err != nil {
				return XLSXLayout{}, "", fmt.Errorf("%w：%w", ErrXLSX, err)
			}
		}
	}
	return p.layout, proof, VerifySource(ctx, f, proof)
}

func (p *xlsxPackage) inspectGrid(ctx context.Context) error {
	if err := p.inspectGridCoordinates(ctx); err != nil {
		return err
	}
	for _, sheet := range p.layout.Sheets {
		if len(p.chartPartsFor(sheet.Name)) > 0 {
			p.layout.ChartSheets = append(p.layout.ChartSheets, sheet.Name)
		}
	}
	p.layout.PivotAxesReady = len(p.pivotOutputs) > 0
	return p.inspectPivotFields(ctx)
}

func (p *xlsxPackage) inspectGridCoordinates(ctx context.Context) error {
	p.layout.GridEditing = true
	if p.layout.Native {
		p.layout.RecalcOnOpen = p.staleFormulaCaches
		return p.checkNativeTableBindings(ctx)
	}
	err := p.inspectWorksheets(ctx)
	if !p.layout.Structured && errors.Is(err, errStructuredXLSX) {
		p.layout.Structured = true
		err = p.inspectWorksheets(ctx)
	}
	if errors.Is(err, errOfficeWorksheet) {
		p.layout.Native, p.layout.Structured, p.layout.RecalcOnOpen = true, true, p.staleFormulaCaches
		if err = p.inspectOfficeParts(ctx); err != nil {
			return err
		}
		return p.checkNativeTableBindings(ctx)
	}
	return err
}

// UpgradeGridLayout rederives coordinate ownership from the digest-bound
// base while keeping the document identity, transactions and artifacts.
func UpgradeGridLayout(ctx context.Context, f *workbook.EditSourceFile, old XLSXLayout) (XLSXLayout, error) {
	if f.Size() != old.Size {
		return XLSXLayout{}, ErrBinding
	}
	p, err := openXLSXPackage(f, f.Size())
	if err == nil {
		err = p.inspect(ctx)
	}
	if err == nil {
		err = p.inspectGrid(ctx)
	}
	if err != nil {
		return XLSXLayout{}, err
	}
	if len(old.Sheets) != len(p.layout.Sheets) || !slices.Equal(old.Rules, p.layout.Rules) {
		return XLSXLayout{}, ErrBinding
	}
	for i, prior := range old.Sheets {
		actual := p.layout.Sheets[i]
		prior.ReadOnly = actual.ReadOnly
		if prior != actual {
			return XLSXLayout{}, ErrBinding
		}
	}
	return p.layout, f.Check()
}
