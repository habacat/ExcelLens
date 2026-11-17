package printing

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type resolved struct {
	paper                       Dimensions
	margins                     MarginUnits
	rowTitles, columnTitles     []Interval
	areas                       []workbook.CellMerge
	rowBreaks, columnBreaks     []int
	defaults                    workbook.PrintGeometryDefaults
	scale                       *big.Rat
	fit                         *Fit
	order                       string
	first                       int64
	centerX, centerY, headings  bool
	headingWidth, headingHeight int64
	origins                     map[string]string
	notices                     []string
}

func (r *resolved) notice(text string) {
	for _, value := range r.notices {
		if value == text {
			return
		}
	}
	if len(r.notices) < formats.PrintNoticeCount {
		r.notices = append(r.notices, text)
	}
}
func (r *resolved) property(s workbook.SourcePrintMetadata, group, name, fallback string) (string, error) {
	value, found := s.Properties[group][name]
	if !found {
		r.origins[group+"."+name] = "schema-default"
		return fallback, nil
	}
	if value.Status != "stored" {
		return "", fmt.Errorf("源%s.%s无法可靠解释，请在预览中明确设置：%s", group, name, value.Reason)
	}
	r.origins[group+"."+name] = "source"
	return strings.TrimSpace(value.Value), nil
}

func resolve(source *workbook.PrintSource, o Options) (*resolved, error) {
	s := source.Settings()
	r := &resolved{origins: map[string]string{}, order: "downThenOver", first: 1}
	for _, text := range s.Notices {
		r.notice(text)
	}
	format, _ := formats.Lookup(source.Format())
	if !s.NamesComplete && (o.RepeatRows == nil || o.RepeatColumns == nil) && !(o.Areas != nil && len(o.Areas) == 0) {
		return nil, errors.New("源名称列表不完整，请明确设置或清除预览重复标题")
	}
	if len(o.Areas) > formats.PrintPaginationAreas {
		return nil, errors.New("打印区域数量超过容量")
	}
	if o.Areas != nil {
		r.areas = append([]workbook.CellMerge{}, o.Areas...)
		r.origins["areas"] = "preview-override"
	} else {
		if !s.NamesComplete {
			return nil, errors.New("源打印名称不完整，请明确选择预览范围")
		}
		for _, name := range s.Names {
			if name.Kind != "area" {
				continue
			}
			if name.Status != "ready" {
				return nil, errors.New("源打印区域未可靠解析，请明确选择预览范围")
			}
			for _, area := range name.Areas {
				if !strings.EqualFold(area.Sheet, source.Sheet()) {
					return nil, errors.New("打印区域不属于当前来源工作表")
				}
				r.areas = append(r.areas, workbook.CellMerge{Row: area.Row, Col: area.Col, EndRow: area.EndRow, EndCol: area.EndCol})
			}
		}
		if len(r.areas) > 0 {
			r.origins["areas"] = "source"
		} else {
			used := source.UsedRange()
			if used.EndRow > 0 && used.EndCol > 0 {
				r.areas = []workbook.CellMerge{used}
			}
			r.origins["areas"] = "indexed-data-range"
			r.notice("源未保存打印区域，预览使用已索引数据范围，可另行选择；不代表格式空白区域全部已涵盖")
		}
	}
	if len(r.areas) > formats.PrintPaginationAreas {
		return nil, errors.New("打印区域数量超过容量")
	}
	for _, area := range r.areas {
		if area.Row < 1 || area.Col < 1 || area.EndRow < area.Row || area.EndCol < area.Col || area.EndRow > format.MaxRows || area.EndCol > format.MaxColumns {
			return nil, errors.New("打印区域源坐标越界")
		}
	}
	if len(r.areas) == 0 {
		return r, nil
	}
	r.rowTitles, r.columnTitles = o.RepeatRows, o.RepeatColumns
	if o.RepeatRows != nil {
		r.origins["repeatRows"] = "preview-override"
	}
	if o.RepeatColumns != nil {
		r.origins["repeatColumns"] = "preview-override"
	}
	for _, name := range s.Names {
		if name.Kind != "titles" || o.RepeatRows != nil && o.RepeatColumns != nil {
			continue
		}
		if name.Status != "ready" {
			return nil, errors.New("源重复标题未可靠解析，请明确设置预览标题")
		}
		for _, area := range name.Areas {
			if !strings.EqualFold(area.Sheet, source.Sheet()) {
				return nil, errors.New("重复标题不属于当前来源工作表")
			}
			if area.Col == 1 && area.EndCol == format.MaxColumns && o.RepeatRows == nil {
				r.rowTitles = append(r.rowTitles, Interval{area.Row, area.EndRow})
				r.origins["repeatRows"] = "source"
			}
			if area.Row == 1 && area.EndRow == format.MaxRows && o.RepeatColumns == nil {
				r.columnTitles = append(r.columnTitles, Interval{area.Col, area.EndCol})
				r.origins["repeatColumns"] = "source"
			}
		}
	}
	var err error
	r.rowTitles, err = normalize(r.rowTitles, format.MaxRows)
	if err != nil {
		return nil, err
	}
	r.columnTitles, err = normalize(r.columnTitles, format.MaxColumns)
	if err != nil {
		return nil, err
	}
	if o.Paper != nil {
		r.paper, err = paperDimensions(*o.Paper)
		r.origins["paper"] = "preview-override"
	} else {
		height, hFound := s.Properties["pageSetup"]["paperHeight"]
		width, wFound := s.Properties["pageSetup"]["paperWidth"]
		if hFound || wFound {
			if !hFound || !wFound || height.Status != "stored" || width.Status != "stored" {
				return nil, errors.New("自定义源纸张尺寸不完整，请明确设置预览纸张")
			}
			r.paper, err = paperDimensions(Paper{width.Value, height.Value})
			r.origins["paper"] = "source"
		} else if value, found := s.Properties["pageSetup"]["paperSize"]; found && value.Status == "stored" {
			code, codeErr := strconv.ParseUint(strings.TrimSpace(value.Value), 10, 32)
			paper, ok := paperSizes[strconv.FormatUint(code, 10)]
			if codeErr != nil || !ok {
				return nil, errors.New("源纸张使用未知或设备专用尺寸，请明确设置预览纸张")
			}
			r.paper, err = paperDimensions(paper)
			r.origins["paper"] = "source"
		} else if o.UsePreviewDefaults {
			r.paper, err = paperDimensions(Paper{"210mm", "297mm"})
			r.origins["paper"] = "preview-default"
			r.notice("预览使用A4纸张；源未提供可靠纸张尺寸")
		} else {
			return nil, errors.New("源未提供可靠纸张尺寸，请设置预览纸张")
		}
	}
	if err != nil {
		return nil, err
	}
	orientation := o.Orientation
	if orientation != "" {
		r.origins["orientation"] = "preview-override"
	} else if value, found := s.Properties["pageSetup"]["orientation"]; found && value.Status == "stored" && strings.TrimSpace(value.Value) != "default" {
		orientation = strings.TrimSpace(value.Value)
		r.origins["orientation"] = "source"
	} else if o.UsePreviewDefaults {
		orientation = "portrait"
		r.origins["orientation"] = "preview-default"
		r.notice("预览使用纵向；源打印机默认方向未确认")
	} else {
		return nil, errors.New("源打印方向未确认，请明确设置预览方向")
	}
	if orientation != "portrait" && orientation != "landscape" {
		return nil, errors.New("预览纸张方向无效")
	}
	if orientation == "portrait" && r.paper.Width > r.paper.Height || orientation == "landscape" && r.paper.Width < r.paper.Height {
		r.paper.Width, r.paper.Height = r.paper.Height, r.paper.Width
	}
	margins := Margins{}
	if o.Margins != nil {
		margins = *o.Margins
		r.origins["margins"] = "preview-override"
	} else {
		for _, field := range []struct {
			name     string
			target   *string
			fallback string
		}{{"left", &margins.Left, "0.7in"}, {"right", &margins.Right, "0.7in"}, {"top", &margins.Top, "0.75in"}, {"bottom", &margins.Bottom, "0.75in"}, {"header", &margins.Header, "0.3in"}, {"footer", &margins.Footer, "0.3in"}} {
			if value, found := s.Properties["pageMargins"][field.name]; found {
				if value.Status != "stored" {
					return nil, errors.New("源页边距无法可靠解释，请设置预览边距")
				}
				*field.target = value.Value + "in"
				r.origins["margin."+field.name] = "source"
			} else if o.UsePreviewDefaults {
				*field.target = field.fallback
				r.origins["margin."+field.name] = "preview-default"
			} else {
				return nil, errors.New("源页边距缺失，请设置预览边距")
			}
		}
	}
	for _, field := range []struct {
		raw    string
		target *int64
	}{{margins.Left, &r.margins.Left}, {margins.Right, &r.margins.Right}, {margins.Top, &r.margins.Top}, {margins.Bottom, &r.margins.Bottom}, {margins.Header, &r.margins.Header}, {margins.Footer, &r.margins.Footer}} {
		*field.target, err = measure(field.raw, true)
		if err != nil {
			return nil, err
		}
	}
	if r.margins.Left >= r.paper.Width || r.margins.Right >= r.paper.Width-r.margins.Left || r.margins.Top >= r.paper.Height || r.margins.Bottom >= r.paper.Height-r.margins.Top {
		return nil, errors.New("页边距占满纸张，请调整预览设置")
	}
	if o.ScalePercent != "" && o.Fit != nil {
		return nil, errors.New("缩放百分比与适配页数不能同时指定")
	}
	fitFlag, err := r.property(s, "pageSetUpPr", "fitToPage", "false")
	if err != nil && o.Fit == nil && o.ScalePercent == "" {
		return nil, err
	}
	if o.Fit != nil {
		value := *o.Fit
		r.fit = &value
		r.origins["scale"] = "preview-override"
	} else if o.ScalePercent == "" && fitFlag == "true" {
		width, err := r.property(s, "pageSetup", "fitToWidth", "1")
		if err != nil {
			return nil, err
		}
		height, err := r.property(s, "pageSetup", "fitToHeight", "1")
		if err != nil {
			return nil, err
		}
		w, wErr := strconv.Atoi(width)
		h, hErr := strconv.Atoi(height)
		if wErr != nil || hErr != nil {
			return nil, errors.New("源适配页数无效")
		}
		r.fit = &Fit{w, h}
		r.origins["scale"] = "source"
	}
	if r.fit != nil {
		if r.fit.Width < 0 || r.fit.Height < 0 || r.fit.Width > formats.PrintPaginationFitPages || r.fit.Height > formats.PrintPaginationFitPages {
			return nil, errors.New("适配页数超过容量")
		}
	} else {
		value := o.ScalePercent
		if value == "" {
			value, err = r.property(s, "pageSetup", "scale", "100")
			if err != nil {
				return nil, err
			}
		} else {
			r.origins["pageSetup.scale"] = "preview-override"
		}
		value = strings.TrimSpace(value)
		if len(value) > formats.PrintGeometryMeasureBytes {
			return nil, errors.New("缩放百分比超长")
		}
		number, err := strconv.ParseFloat(value, 64)
		if err != nil || number < 1 || number > 400 || strings.ContainsAny(value, "xXpP/_") {
			return nil, errors.New("预览缩放百分比须在1到400之间")
		}
		r.scale, _ = new(big.Rat).SetString(value)
		if r.scale == nil {
			return nil, errors.New("缩放百分比无效")
		}
		r.scale.Quo(r.scale, big.NewRat(100, 1))
	}
	order, err := r.property(s, "pageSetup", "pageOrder", "downThenOver")
	if err != nil && o.PageOrder == "" {
		return nil, err
	}
	if o.PageOrder != "" {
		order = o.PageOrder
		r.origins["pageOrder"] = "preview-override"
	}
	if order != "downThenOver" && order != "overThenDown" {
		return nil, errors.New("打印顺序无效")
	}
	r.order = order
	if o.FirstPageNumber != nil {
		r.first = *o.FirstPageNumber
		r.origins["firstPageNumber"] = "preview-override"
	} else {
		use, err := r.property(s, "pageSetup", "useFirstPageNumber", "false")
		if err != nil {
			return nil, err
		}
		if use == "true" {
			number, err := r.property(s, "pageSetup", "firstPageNumber", "1")
			if err != nil {
				return nil, err
			}
			r.first, err = strconv.ParseInt(number, 10, 64)
			if err != nil {
				return nil, errors.New("源起始页码无效")
			}
		}
	}
	if r.first < formats.PrintFirstPageMin || r.first > formats.PrintFirstPageMax {
		return nil, errors.New("起始页码超出源格式范围")
	}
	for _, field := range []struct {
		name   string
		target *bool
	}{{"horizontalCentered", &r.centerX}, {"verticalCentered", &r.centerY}, {"headings", &r.headings}} {
		value, err := r.property(s, "printOptions", field.name, "false")
		if err != nil {
			return nil, err
		}
		if value != "true" && value != "false" {
			return nil, errors.New("源打印选项无效")
		}
		*field.target = value == "true"
	}
	if r.headings {
		digits := len(strconv.Itoa(max(1, source.UsedRange().EndRow)))
		r.headingWidth = int64((float64(digits)*source.Geometry().Summary().DigitWidth96 + 12) * 0.75 * float64(formats.PrintGeometryUnitsPerPoint))
		r.headingHeight = 12 * formats.PrintGeometryUnitsPerPoint
		r.origins["headingSize"] = "preview-default"
		r.notice("打印行列标题按预览字体尺寸估算，实际打印机可能不同")
	}
	if o.MissingRowHeight != "" {
		r.defaults.Row = &workbook.PrintGeometryDefault{Value: o.MissingRowHeight, Unit: "point"}
		r.origins["missingRowHeight"] = "preview-override"
	} else if o.UsePreviewDefaults {
		r.defaults.Row = &workbook.PrintGeometryDefault{Value: "15", Unit: "point"}
		r.origins["missingRowHeight"] = "preview-default"
	}
	if o.MissingColumnWidth != "" {
		r.defaults.Column = &workbook.PrintGeometryDefault{Value: o.MissingColumnWidth, Unit: "point"}
		r.origins["missingColumnWidth"] = "preview-override"
	} else if o.UsePreviewDefaults {
		r.defaults.Column = &workbook.PrintGeometryDefault{Value: "8", Unit: "base-char"}
		r.origins["missingColumnWidth"] = "preview-default"
	}
	if r.fit != nil || o.IgnoreManualBreaks {
		r.origins["manualBreaks"] = "ignored-by-fit-or-preview"
		if len(s.RowBreaks)+len(s.ColBreaks) > 0 {
			r.notice("适配页数或预览设置忽略源手工分页，原定义保留")
		}
	} else {
		r.origins["manualBreaks"] = "office-compatible-source"
		r.rowBreaks, err = manualBreaks(s.RowBreaks, format.MaxRows)
		if err != nil {
			return nil, err
		}
		r.columnBreaks, err = manualBreaks(s.ColBreaks, format.MaxColumns)
		if err != nil {
			return nil, err
		}
		if len(r.rowBreaks)+len(r.columnBreaks) > 0 {
			r.notice("源手工分页按Office兼容规则跨整页应用，原始局部范围保留")
		}
	}
	return r, nil
}

func paperDimensions(paper Paper) (Dimensions, error) {
	w, err := measure(paper.Width, false)
	if err != nil {
		return Dimensions{}, err
	}
	h, err := measure(paper.Height, false)
	if err != nil {
		return Dimensions{}, err
	}
	if w <= 0 || h <= 0 {
		return Dimensions{}, errors.New("纸张尺寸必须大于零")
	}
	return Dimensions{w, h}, nil
}
func manualBreaks(input []workbook.SourcePrintBreak, maximum int) ([]int, error) {
	result := []int{}
	for _, item := range input {
		if item.Manual == nil || !*item.Manual {
			continue
		}
		if item.Status != "stored" || item.Index < 0 || item.Index >= maximum {
			return nil, errors.New("源手工分页无法可靠解释，请调整预览设置")
		}
		result = append(result, item.Index+1)
	}
	sort.Ints(result)
	n := 0
	for _, v := range result {
		if n == 0 || result[n-1] != v {
			result[n] = v
			n++
		}
	}
	return result[:n], nil
}

// Source paper IDs are device codes; unknown/device-specific IDs require an
// explicit size. Units remain exact strings until bounded rational conversion.
var paperSizes = map[string]Paper{
	"1": {"8.5in", "11in"}, "2": {"8.5in", "11in"}, "3": {"11in", "17in"}, "4": {"17in", "11in"}, "5": {"8.5in", "14in"}, "6": {"5.5in", "8.5in"}, "7": {"7.25in", "10.5in"}, "8": {"297mm", "420mm"}, "9": {"210mm", "297mm"}, "10": {"210mm", "297mm"}, "11": {"148mm", "210mm"}, "12": {"250mm", "354mm"}, "13": {"182mm", "257mm"}, "14": {"8.5in", "13in"}, "15": {"215mm", "275mm"}, "16": {"10in", "14in"}, "17": {"11in", "17in"}, "18": {"8.5in", "11in"}, "27": {"110mm", "220mm"}, "28": {"162mm", "229mm"}, "29": {"324mm", "458mm"}, "30": {"229mm", "324mm"}, "31": {"114mm", "162mm"}, "32": {"114mm", "229mm"}, "37": {"3.875in", "7.5in"}, "42": {"250mm", "353mm"}, "43": {"100mm", "148mm"}, "66": {"420mm", "594mm"}, "70": {"105mm", "148mm"}, "75": {"11in", "8.5in"}, "76": {"420mm", "297mm"}, "77": {"297mm", "210mm"}, "78": {"210mm", "148mm"}, "79": {"364mm", "257mm"}, "80": {"257mm", "182mm"}, "81": {"148mm", "100mm"}, "82": {"200mm", "148mm"}, "83": {"148mm", "105mm"}, "88": {"128mm", "182mm"}, "89": {"182mm", "128mm"}, "93": {"146mm", "215mm"}, "94": {"97mm", "151mm"}, "95": {"97mm", "151mm"},
}
