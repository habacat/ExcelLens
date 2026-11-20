package editfile

import (
	"context"
	"encoding/xml"
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

const secondsPerDay int64 = 86400

var clockText = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}(?::[0-9]{2}(?:\.[0-9]{1,3})?)?$`)
var errCalendarCompatibility = errors.New("此工作簿的日期系统无法用于序列或日期规则")
var errCalendarSetting = errors.New("工作簿日期系统无效")

func calendarProperties(el xml.StartElement) (bool, error) {
	if compatibility := attribute(el, "", "dateCompatibility"); compatibility != "" && !isTrue(compatibility) {
		return false, errCalendarCompatibility
	}
	value := attribute(el, "", "date1904")
	if value != "" && !ruleBool(value) {
		return false, errCalendarSetting
	}
	return isTrue(value), nil
}

func workbookCalendar(ctx context.Context, f *workbook.EditSourceFile, v editdoc.Value) (bool, string, error) {
	p, err := openXLSXPackage(f, f.Size())
	if err != nil {
		return false, "", err
	}
	data, err := p.readPart(ctx, "xl/workbook.xml")
	if err != nil {
		return false, "", err
	}
	var date1904 bool
	err = walkXML(ctx, data, func(e xmlEvent) error {
		el, ok := e.token.(xml.StartElement)
		if !ok || el.Name != (xml.Name{Space: mainNS, Local: "workbookPr"}) || e.parent() != "workbook" {
			return nil
		}
		var err error
		date1904, err = calendarProperties(el)
		return err
	})
	if err == nil && v.BuiltinFormat != 0 {
		switch v.BuiltinFormat {
		case 14:
			return date1904, "date", nil
		case 18:
			return date1904, "time", nil
		}
		return date1904, "", nil
	}
	style := v.NumberFormatStyle()
	if err != nil || style == "" || p.files["xl/styles.xml"] == nil {
		return date1904, "", err
	}
	data, err = p.readPart(ctx, "xl/styles.xml")
	if err != nil {
		return false, "", err
	}
	index, err := strconv.Atoi(style)
	if err != nil {
		return false, "", err
	}
	kind, err := workbook.XLSXCalendarStyle(data, index)
	return date1904, kind, err
}

func minExcelDate(date1904 bool) int64 {
	if date1904 {
		return 0
	}
	return 1
}

func maxExcelDate(date1904 bool) int64 {
	return calculator.ExcelDateSerial(time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), date1904)
}

func parseISODate(raw string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil || t.Year() < 1 || t.Format(time.DateOnly) != raw {
		return time.Time{}, errors.New("日期请用 YYYY-MM-DD，先确认日/月顺序")
	}
	return t, nil
}

func parseClock(raw string) (time.Duration, error) {
	if clockText.MatchString(raw) {
		if len(raw) == 5 {
			raw += ":00"
		}
		t, err := time.Parse(time.TimeOnly, raw)
		if err == nil {
			return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second + time.Duration(t.Nanosecond()), nil
		}
	}
	return 0, errors.New("时间请用 HH:MM 或 HH:MM:SS，可附三位小数")
}

func DateRuleBounds(ctx context.Context, f *workbook.EditSourceFile, rule editdoc.Rule) (editdoc.Rule, error) {
	date1904, _, err := workbookCalendar(ctx, f, editdoc.Value{})
	if err != nil {
		return rule, err
	}
	for _, raw := range []*string{&rule.First, &rule.Second} {
		if *raw == "" {
			continue
		}
		day, err := dateRuleBound(*raw, date1904)
		if err != nil {
			return rule, err
		}
		*raw = strconv.FormatInt(day, 10)
	}
	return rule, nil
}

func dateRuleBound(raw string, date1904 bool) (int64, error) {
	var day int64
	if n, ok := ruleNumber(raw); ok && n.IsInt() && n.Num().IsInt64() {
		day = n.Num().Int64()
	} else if raw == "1900-02-29" && !date1904 {
		day = 60
	} else {
		t, err := parseISODate(raw)
		if err != nil {
			return 0, err
		}
		day = calculator.ExcelDateSerial(t, date1904)
	}
	if day < minExcelDate(date1904) || day > maxExcelDate(date1904) {
		return 0, errors.New("日期超出当前工作簿的日期系统范围")
	}
	return day, nil
}

func excelDateText(day int64, date1904 bool) string {
	base := time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else if day == 60 {
		return "1900-02-29"
	} else if day > 60 {
		day--
	}
	return base.AddDate(0, 0, int(day)).Format(time.DateOnly)
}

// CalendarFill is called only for an explicit fill preview. A native serial
// requires the corresponding source format. Text is parsed as unambiguous ISO
// and stays text. Each output keeps the spreadsheet date system of its source.
func CalendarFill(ctx context.Context, f *workbook.EditSourceFile, start editdoc.Value, mode, stepText string, count int) ([]editdoc.Value, string, error) {
	step, err := strconv.ParseInt(stepText, 10, 32)
	if err != nil || step == 0 || count < 1 || count > formats.EditBatchCells {
		return nil, "", errors.New("步长请输入非零整数")
	}
	native := start.Kind == "number"
	var date1904 bool
	if native {
		var kind string
		date1904, kind, err = workbookCalendar(ctx, f, start)
		if err != nil {
			return nil, "", err
		}
		if kind != mode {
			return nil, "", errors.New("起点数值需使用相符的普通日期或时间样式")
		}
	} else if start.Kind != "text" {
		return nil, "", errors.New("起点需要日期、时间或对应的文本")
	}
	if mode == "date" {
		return calendarDates(start, native, date1904, step, count)
	}
	if mode == "time" {
		return calendarTimes(start, native, step, count)
	}
	return nil, "", editdoc.ErrInvalid
}

func calendarDates(start editdoc.Value, native, date1904 bool, step int64, count int) ([]editdoc.Value, string, error) {
	var first, minDay, maxDay int64
	if native {
		var err error
		first, err = dateRuleBound(start.Raw, date1904)
		if err != nil {
			return nil, "", err
		}
		minDay, maxDay = minExcelDate(date1904), maxExcelDate(date1904)
	} else {
		t, err := parseISODate(start.Raw)
		if err != nil {
			return nil, "", err
		}
		first = t.Unix() / secondsPerDay
		minDay = time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC).Unix() / secondsPerDay
		maxDay = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC).Unix() / secondsPerDay
	}
	end := first + step*int64(count)
	if end < minDay || end > maxDay {
		return nil, "", errors.New("日期序列超出可保存的日期范围")
	}
	out := make([]editdoc.Value, count)
	for i := range out {
		day := first + step*int64(i+1)
		if native {
			out[i] = start
			out[i].Raw = strconv.FormatInt(day, 10)
		} else {
			out[i] = editdoc.Value{Kind: "text", Raw: time.Unix(day*secondsPerDay, 0).UTC().Format(time.DateOnly)}
		}
	}
	if native {
		return out, excelDateText(end, date1904), nil
	}
	return out, out[count-1].Raw, nil
}

func calendarTimes(start editdoc.Value, native bool, step int64, count int) ([]editdoc.Value, string, error) {
	one := big.NewRat(1, 1)
	first := new(big.Rat)
	if native {
		var ok bool
		first, ok = ruleNumber(start.Raw)
		if !ok || first.Sign() < 0 || first.Cmp(one) >= 0 {
			return nil, "", errors.New("起始时间需要是当天的时间值")
		}
	} else {
		d, err := parseClock(start.Raw)
		if err != nil {
			return nil, "", err
		}
		first.SetFrac64(int64(d), int64(24*time.Hour))
	}
	end := new(big.Rat).Add(first, big.NewRat(step*int64(count), secondsPerDay))
	if end.Sign() < 0 || end.Cmp(one) >= 0 {
		return nil, "", errors.New("时间序列跨越午夜，请缩短范围或调整步长")
	}
	out := make([]editdoc.Value, count)
	for i := range out {
		value := new(big.Rat).Add(first, big.NewRat(step*int64(i+1), secondsPerDay))
		if native {
			raw := strings.TrimSuffix(strings.TrimRight(value.FloatString(15), "0"), ".")
			if raw == "1" || raw == "0" && value.Sign() != 0 {
				return nil, "", errors.New("时间数值舍入后超出当天范围或失去精度")
			}
			out[i] = start
			out[i].Raw = raw
		} else {
			out[i] = editdoc.Value{Kind: "text", Raw: clockDisplay(value)}
		}
	}
	return out, clockDisplay(end), nil
}

func clockDisplay(serial *big.Rat) string {
	const dayMS = secondsPerDay * 1000
	ms := new(big.Rat).Mul(serial, big.NewRat(dayMS, 1))
	units, _ := strconv.ParseInt(ms.FloatString(0), 10, 64)
	return time.UnixMilli(min(units, dayMS-1)).UTC().Format("15:04:05.999")
}
