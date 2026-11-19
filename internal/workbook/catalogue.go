package workbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"excellens/internal/formats"
)

// Catalogue contains only source metadata. No values or formula evaluations
// are needed to resolve a fixed absolute range. A partial list stays explicit.
type Catalogue struct {
	Items           []CatalogueItem `json:"items"`
	Complete        bool            `json:"complete"`
	NamesIncomplete bool            `json:"namesIncomplete,omitempty"`
	Notices         []string        `json:"notices,omitempty"`
}

type CatalogueArea struct {
	Sheet  string `json:"sheet"`
	Start  string `json:"start"`
	End    string `json:"end"`
	Row    int    `json:"row"`
	Col    int    `json:"col"`
	EndRow int    `json:"endRow"`
	EndCol int    `json:"endCol"`
}

type CatalogueColumn struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type CatalogueTable struct {
	Ref        string            `json:"ref"`
	HeaderRows int               `json:"headerRows"`
	TotalRows  int               `json:"totalRows"`
	Columns    []CatalogueColumn `json:"columns,omitempty"`
	Part       string            `json:"part,omitempty"`
	RelationID string            `json:"relationId,omitempty"`
	SourceID   string            `json:"sourceId,omitempty"`
	Verified   bool              `json:"verified"`
}

type CatalogueItem struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Scope      string          `json:"scope,omitempty"`
	Sheet      string          `json:"sheet,omitempty"`
	ScopeID    string          `json:"scopeId,omitempty"`
	Hidden     bool            `json:"hidden,omitempty"`
	Comment    string          `json:"comment,omitempty"`
	Definition string          `json:"definition"`
	TokenHex   string          `json:"tokenHex,omitempty"`
	Status     string          `json:"status"`
	Reason     string          `json:"reason,omitempty"`
	Areas      []CatalogueArea `json:"areas,omitempty"`
	Table      *CatalogueTable `json:"table,omitempty"`

	// BIFF aliases bind to a Lbl record ordinal, not a name lookup. TokenHex
	// remains public evidence; this immutable source-only plan preserves identity.
	biffTerms []catalogueBIFFTerm
}

type catalogueBuilder struct {
	value Catalogue
	bytes int
}

func newCatalogueBuilder() *catalogueBuilder {
	return &catalogueBuilder{value: Catalogue{Items: []CatalogueItem{}, Complete: true}}
}

func (b *catalogueBuilder) notice(message string) {
	for _, old := range b.value.Notices {
		if old == message {
			return
		}
	}
	if len(b.value.Notices) < 32 {
		b.value.Notices = append(b.value.Notices, message)
	}
	b.value.Complete = false
}

func (b *catalogueBuilder) add(item CatalogueItem) bool {
	if item.ID == "" {
		item.ID = strconv.Itoa(len(b.value.Items) + 1)
	}
	if len(item.Name) > formats.CatalogueDefinitionMax || len(item.Definition) > formats.CatalogueDefinitionMax || len(item.Comment) > formats.CatalogueDefinitionMax {
		b.value.NamesIncomplete = b.value.NamesIncomplete || item.Kind == "name"
		b.notice("部分名称或定义超过单项读取容量，目录不完整")
		return false
	}
	data, err := json.Marshal(item)
	if err != nil || len(b.value.Items) >= formats.CatalogueEntries || len(data) > formats.CatalogueBytes-b.bytes {
		b.value.NamesIncomplete = b.value.NamesIncomplete || item.Kind == "name"
		b.notice("名称及表对象目录超过读取容量，目录不完整")
		return false
	}
	b.bytes += len(data)
	b.value.Items = append(b.value.Items, item)
	return true
}

func catalogueNamespace(value string) bool {
	return value == "" || value == "http://schemas.openxmlformats.org/spreadsheetml/2006/main" || value == "http://purl.oclc.org/ooxml/spreadsheetml/main"
}

func catalogueScopeKey(scope, name string) string {
	return strings.ToLower(scope) + "\x00" + strings.ToLower(name)
}

type catalogueResolver struct {
	w               *Workbook
	ctx             context.Context
	items           []CatalogueItem
	names           map[string][]int
	tables          map[string][]int
	ids             map[string][]int
	sheets          map[string][]SheetMeta
	work            int
	namesIncomplete bool
}

// ResolveCatalogue recomputes public status from unchanged definitions. Alias
// graphs, unions and structured table slices use bounded metadata work only.
func (w *Workbook) ResolveCatalogue(input Catalogue) Catalogue {
	result, _ := w.resolveCatalogue(context.Background(), input)
	return result
}

func (w *Workbook) resolveCatalogue(ctx context.Context, input Catalogue) (Catalogue, error) {
	if err := ctx.Err(); err != nil {
		return Catalogue{}, err
	}
	result := input
	result.Items = append([]CatalogueItem(nil), input.Items...)
	result.Notices = append([]string(nil), input.Notices...)
	r := catalogueResolver{w: w, ctx: ctx, items: result.Items, names: make(map[string][]int), tables: make(map[string][]int), ids: make(map[string][]int), sheets: make(map[string][]SheetMeta), namesIncomplete: input.NamesIncomplete}
	for _, sheet := range w.Sheets {
		key := catalogueSheetKey(sheet.Name)
		r.sheets[key] = append(r.sheets[key], sheet)
	}
	for i, item := range r.items {
		if item.ID != "" {
			r.ids[item.ID] = append(r.ids[item.ID], i)
		}
		if item.Kind == "name" {
			key := catalogueScopeKey(item.Scope, item.Name)
			r.names[key] = append(r.names[key], i)
		} else if item.Kind == "table" && item.Status != "invalid" && item.Status != "unavailable" {
			r.tables[r.tableKey(item.Name)] = append(r.tables[r.tableKey(item.Name)], i)
		}
	}
	encodedBytes := 0
	for i := range r.items {
		if err := ctx.Err(); err != nil {
			return Catalogue{}, err
		}
		item := &r.items[i]
		item.Areas = nil
		if item.Status != "invalid" && item.Status != "unavailable" {
			areas, err := r.item(i, make(map[int]bool), 0)
			if err != nil {
				item.Status, item.Reason = "unresolved", err.Error()
			} else {
				item.Status, item.Reason, item.Areas = "ready", "", areas
			}
		}
		encoded, _ := json.Marshal(item)
		if len(encoded) > formats.CatalogueBytes-encodedBytes && len(item.Areas) > 0 {
			item.Status, item.Reason, item.Areas = "unresolved", "区域展开超过目录显示容量，原定义保留", nil
			encoded, _ = json.Marshal(item)
		}
		if len(encoded) > formats.CatalogueBytes-encodedBytes {
			result.Items = result.Items[:i]
			result.Complete = false
			result.Notices = append(result.Notices, "解析结果超过目录容量，目录不完整")
			break
		}
		encodedBytes += len(encoded)
		if r.work >= formats.CatalogueResolveWork {
			result.Complete = false
			result.Notices = append(result.Notices, "名称引用解析达到本次工作容量，未解析项保留原定义")
			for j := i + 1; j < len(r.items); j++ {
				r.items[j].Areas = nil
				if r.items[j].Status != "invalid" && r.items[j].Status != "unavailable" {
					r.items[j].Status, r.items[j].Reason, r.items[j].Areas = "unresolved", "尚未解析：本次工作容量已用完", nil
				}
			}
			break
		}
	}
	bounded := newCatalogueBuilder()
	bounded.value.Complete, bounded.value.Notices = result.Complete, result.Notices
	bounded.value.NamesIncomplete = result.NamesIncomplete
	for _, item := range result.Items {
		if !bounded.add(item) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return Catalogue{}, err
	}
	return bounded.value, nil
}

// ResolveXLSXNameRanges applies the same fixed-range, alias and scope rules as
// the read-only name catalogue to definitions collected by copy preflight.
func ResolveXLSXNameRanges(ctx context.Context, sheets []SheetMeta, names []CatalogueItem) (Catalogue, error) {
	w := Workbook{Format: "xlsx", Sheets: sheets}
	return w.resolveCatalogue(ctx, Catalogue{Items: names, Complete: true})
}

func (r *catalogueResolver) item(index int, visited map[int]bool, depth int) ([]CatalogueArea, error) {
	if err := r.spend(1); err != nil {
		return nil, err
	}
	if depth > formats.CatalogueDepth {
		return nil, errors.New("名称引用层数或工作容量超限")
	}
	if visited[index] {
		return nil, errors.New("名称存在循环引用")
	}
	item := r.items[index]
	if item.Status == "invalid" || item.Status == "unavailable" {
		return nil, errors.New(item.Reason)
	}
	visited[index] = true
	defer delete(visited, index)
	if item.Kind == "table" {
		if len(r.tables[r.tableKey(item.Name)]) != 1 {
			return nil, errors.New("存在重复表对象名称，不能自动选定")
		}
		return r.table(item, "#All")
	}
	if r.namesIncomplete {
		return nil, errors.New("名称列表未完整读取，不能排除重复名称或作用域遮盖，暂不自动定位")
	}
	if len(r.names[catalogueScopeKey(item.Scope, item.Name)]) != 1 {
		return nil, errors.New("同一作用域中存在重复名称，不能自动选定")
	}
	if item.TokenHex != "" {
		return r.biff(item, visited, depth+1)
	}
	return r.expression(item.Definition, item.Scope, visited, depth+1)
}

func (r *catalogueResolver) spend(work int) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if work > formats.CatalogueResolveWork-r.work {
		r.work = formats.CatalogueResolveWork
		return errors.New("名称或表对象解析超过本次工作容量")
	}
	r.work += work
	return nil
}

func (r *catalogueResolver) biff(item CatalogueItem, visited map[int]bool, depth int) ([]CatalogueArea, error) {
	if len(item.biffTerms) == 0 {
		return nil, errors.New("BIFF8名称的原始记录绑定不可用，不能按显示文字重新推断引用")
	}
	areas := []CatalogueArea{}
	for _, term := range item.biffTerms {
		var next []CatalogueArea
		var err error
		if term.nameID != "" {
			indexes := r.ids[term.nameID]
			if len(indexes) != 1 || r.items[indexes[0]].Kind != "name" {
				return nil, errors.New("BIFF8名称引用的原始记录未读取或不唯一")
			}
			next, err = r.item(indexes[0], visited, depth)
		} else {
			next, err = r.expression(term.reference, item.Scope, visited, depth)
		}
		if err != nil {
			return nil, err
		}
		areas = append(areas, next...)
		if len(areas) > formats.CatalogueAreas {
			return nil, errors.New("BIFF8名称展开后的区域数量超过容量")
		}
	}
	return areas, nil
}

func splitCatalogueUnion(value string) ([]string, error) {
	parts := []string{}
	quoted := false
	start := 0
	for i := 0; i < len(value); i++ {
		if value[i] == '\'' {
			if quoted && i+1 < len(value) && value[i+1] == '\'' {
				i++
				continue
			}
			quoted = !quoted
		} else if value[i] == ',' && !quoted {
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	if quoted {
		return nil, errors.New("工作表名称引号未闭合")
	}
	parts = append(parts, value[start:])
	if len(parts) > formats.CatalogueAreas {
		return nil, errors.New("名称包含的区域数量超过容量")
	}
	return parts, nil
}

// SplitSheetReference separates a local sheet qualifier, including escaped
// quotes, from its address. External and three-dimensional targets are rejected.
func SplitSheetReference(value string) (sheet, address string, qualified bool, err error) {
	quote, separator := false, -1
	for i := 0; i < len(value); i++ {
		if value[i] == '\'' {
			if quote && i+1 < len(value) && value[i+1] == '\'' {
				i++
				continue
			}
			quote = !quote
		} else if value[i] == '!' && !quote {
			if separator >= 0 {
				return "", "", false, errors.New("引用含多个工作表分隔符")
			}
			separator = i
		}
	}
	if quote {
		return "", "", false, errors.New("工作表名称引号未闭合")
	}
	if separator < 0 {
		return "", value, false, nil
	}
	sheet, address = value[:separator], value[separator+1:]
	if len(sheet) >= 2 && sheet[0] == '\'' && sheet[len(sheet)-1] == '\'' {
		sheet = strings.ReplaceAll(sheet[1:len(sheet)-1], "''", "'")
	}
	if strings.ContainsAny(sheet, "[]") {
		return "", "", false, errors.New("外部工作簿引用不自动定位")
	}
	if strings.Contains(sheet, ":") {
		return "", "", false, errors.New("三维工作表引用尚不支持定位")
	}
	return sheet, address, true, nil
}

func (r *catalogueResolver) expression(expression, scope string, visited map[int]bool, depth int) ([]CatalogueArea, error) {
	expression = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(expression), "="))
	parts, err := splitCatalogueUnion(expression)
	if err != nil {
		return nil, err
	}
	areas := []CatalogueArea{}
	for _, part := range parts {
		if err := r.spend(1 + len(part)/64); err != nil {
			return nil, err
		}
		part = strings.TrimSpace(part)
		sheet, address, qualified, err := SplitSheetReference(part)
		if err != nil {
			return nil, err
		}
		if qualified {
			meta, ok := r.sheet(sheet)
			if !ok {
				return nil, errors.New("引用的工作表不存在或不是可读取的单元格工作表")
			}
			sheet = meta.Name
		} else {
			sheet = scope
		}
		if strings.Contains(address, "$") {
			if sheet == "" {
				return nil, errors.New("未限定工作表的名称不能确定固定位置")
			}
			area, err := r.absoluteArea(sheet, address)
			if err != nil {
				return nil, err
			}
			areas = append(areas, area)
		} else {
			key := r.tableKey(address)
			matches := r.names[catalogueScopeKey(sheet, address)]
			if len(matches) == 0 && (!qualified || sheet != "") {
				matches = r.names[catalogueScopeKey("", address)]
			}
			if len(matches) > 1 {
				return nil, errors.New("引用了同一作用域中的重复名称")
			}
			var next []CatalogueArea
			if len(matches) == 1 {
				next, err = r.item(matches[0], visited, depth)
			} else if table, slice, ok := catalogueStructuredReference(address); ok {
				indexes := r.tables[r.tableKey(table)]
				if len(indexes) != 1 {
					return nil, errors.New("引用的表对象未读取或名称不唯一")
				}
				if qualified && !strings.EqualFold(r.items[indexes[0]].Sheet, sheet) {
					return nil, errors.New("表对象所在工作表与限定的工作表不一致")
				}
				next, err = r.table(r.items[indexes[0]], slice)
			} else if indexes := r.tables[key]; len(indexes) == 1 {
				if qualified && !strings.EqualFold(r.items[indexes[0]].Sheet, sheet) {
					return nil, errors.New("表对象所在工作表与限定的工作表不一致")
				}
				next, err = r.table(r.items[indexes[0]], "#Data")
			} else {
				return nil, errors.New("常量、相对引用或复杂表达式仅保留定义，不自动定位")
			}
			if err != nil {
				return nil, err
			}
			areas = append(areas, next...)
		}
		if len(areas) > formats.CatalogueAreas {
			return nil, errors.New("名称展开后的区域数量超过容量")
		}
	}
	if len(areas) == 0 {
		return nil, errors.New("名称未包含可定位区域")
	}
	return areas, nil
}

func catalogueStructuredReference(value string) (string, string, bool) {
	i := strings.IndexByte(value, '[')
	if i <= 0 || !strings.HasSuffix(value, "]") || strings.ContainsAny(value[i+1:len(value)-1], "[]") {
		return "", "", false
	}
	return value[:i], value[i+1 : len(value)-1], true
}

func (r *catalogueResolver) tableKey(name string) string {
	if r.w.Format == "xls" {
		return name // TableFeatureType.rgbName is case-sensitive in every locale.
	}
	return strings.ToLower(name)
}

func (r *catalogueResolver) table(item CatalogueItem, slice string) ([]CatalogueArea, error) {
	if item.Table == nil {
		return nil, errors.New("表对象定义未能读取")
	}
	if !item.Table.Verified {
		return nil, errors.New("表对象引用尚未确认")
	}
	area, err := r.absoluteArea(item.Sheet, absoluteCatalogueRef(item.Table.Ref))
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(slice) {
	case "#all":
	case "#data":
		area.Row += item.Table.HeaderRows
		area.EndRow -= item.Table.TotalRows
	case "#headers":
		if item.Table.HeaderRows != 1 {
			return nil, errors.New("此表对象没有表头行")
		}
		area.EndRow = area.Row
	case "#totals":
		if item.Table.TotalRows != 1 {
			return nil, errors.New("此表对象没有合计行")
		}
		area.Row = area.EndRow
	default:
		column := -1
		for i, field := range item.Table.Columns {
			if err := r.spend(1 + min(len(field.Name), len(slice))/64); err != nil {
				return nil, err
			}
			if strings.EqualFold(field.Name, slice) {
				if column >= 0 {
					return nil, errors.New("表对象列名重复，不能确定唯一列")
				}
				column = i
			}
		}
		if column < 0 {
			return nil, errors.New("表对象列名未能确定或结构化引用尚不支持")
		}
		area.Col += column
		area.EndCol = area.Col
		area.Row += item.Table.HeaderRows
		area.EndRow -= item.Table.TotalRows
	}
	if area.Row > area.EndRow {
		return nil, errors.New("此表对象没有数据行")
	}
	area.Start, area.End = ColumnName(area.Col)+strconv.Itoa(area.Row), ColumnName(area.EndCol)+strconv.Itoa(area.EndRow)
	return []CatalogueArea{area}, nil
}

func absoluteCatalogueRef(ref string) string {
	parts := strings.Split(ref, ":")
	for i, part := range parts {
		part = strings.ReplaceAll(part, "$", "")
		col, row, err := ParseCellRef(part)
		if err != nil {
			return ref
		}
		parts[i] = "$" + ColumnName(col) + "$" + strconv.Itoa(row)
	}
	return strings.Join(parts, ":")
}

// SimpleFold uses the same Unicode case equivalence as FindSheet's EqualFold,
// with one metadata lookup per reference instead of rescanning every sheet.
func catalogueSheetKey(value string) string {
	return strings.Map(func(ch rune) rune {
		next := unicode.SimpleFold(ch)
		for next > ch {
			ch, next = next, unicode.SimpleFold(next)
		}
		return next
	}, value)
}

func (r *catalogueResolver) sheet(name string) (SheetMeta, bool) {
	items := r.sheets[catalogueSheetKey(name)]
	if len(items) != 1 {
		return SheetMeta{}, false
	}
	return items[0], true
}

func (r *catalogueResolver) absoluteArea(sheet, reference string) (CatalogueArea, error) {
	meta, ok := r.sheet(sheet)
	if !ok {
		return CatalogueArea{}, errors.New("引用的工作表不存在")
	}
	return AbsoluteSourceArea(r.w.Format, meta.Name, reference)
}

// AbsoluteSourceArea parses an absolute cell, row or column range within the
// specified source format. The caller supplies the resolved sheet name.
func AbsoluteSourceArea(format, sheet, reference string) (CatalogueArea, error) {
	capability, ok := formats.Lookup(format)
	if !ok || capability.MaxRows < 1 {
		return CatalogueArea{}, errors.New("源格式没有可定位的单元格容量")
	}
	parts := strings.Split(reference, ":")
	if len(parts) < 1 || len(parts) > 2 {
		return CatalogueArea{}, errors.New("单元格区域表达式无效")
	}
	parse := func(value string) (row, col, kind int, err error) {
		if !strings.HasPrefix(value, "$") {
			return 0, 0, 0, errors.New("相对引用需要活动单元格上下文，不自动定位")
		}
		plain := strings.ReplaceAll(value, "$", "")
		if c, r, e := ParseCellRef(plain); e == nil {
			if strings.Count(value, "$") != 2 || value != "$"+plain[:len(plain)-len(strconv.Itoa(r))]+"$"+strconv.Itoa(r) {
				return 0, 0, 0, errors.New("仅支持完整绝对引用")
			}
			return r, c, 1, nil
		}
		if n, e := strconv.Atoi(plain); e == nil && n > 0 && "$"+strconv.Itoa(n) == value {
			return n, 0, 2, nil
		}
		if c, r, e := ParseCellRef(plain + "1"); e == nil && r == 1 && strings.ToUpper(plain) == ColumnName(c) && "$"+plain == value {
			return 0, c, 3, nil
		}
		return 0, 0, 0, errors.New("绝对引用坐标无效")
	}
	r1, c1, k1, err := parse(parts[0])
	if err != nil {
		return CatalogueArea{}, err
	}
	r2, c2, k2 := r1, c1, k1
	if len(parts) == 2 {
		r2, c2, k2, err = parse(parts[1])
		if err != nil {
			return CatalogueArea{}, err
		}
	}
	if k1 != k2 || (k1 != 1 && len(parts) != 2) {
		return CatalogueArea{}, errors.New("整行或整列必须使用明确的范围")
	}
	if k1 == 2 {
		c1, c2 = 1, capability.MaxColumns
	}
	if k1 == 3 {
		r1, r2 = 1, capability.MaxRows
	}
	if r1 < 1 || r2 < r1 || r2 > capability.MaxRows || c1 < 1 || c2 < c1 || c2 > capability.MaxColumns {
		return CatalogueArea{}, fmt.Errorf("引用范围超出%s容量或方向无效", format)
	}
	return CatalogueArea{Sheet: sheet, Start: ColumnName(c1) + strconv.Itoa(r1), End: ColumnName(c2) + strconv.Itoa(r2), Row: r1, Col: c1, EndRow: r2, EndCol: c2}, nil
}
