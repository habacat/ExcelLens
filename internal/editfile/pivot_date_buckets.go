package editfile

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

type dateBuckets struct {
	cacheDateRange
	start, end time.Time
	values     []pivotValue
}

// Excel advances a new upper bound at midnight to the following midnight.
// Later times inside that day keep the boundary already established. A noon
// encountered before midnight on the same date keeps its exact time instead.
type pivotDateExtent struct {
	first, last string
}

func (b *pivotDateExtent) add(value string) {
	if b.first == "" || value < b.first {
		b.first = value
	}
	if value < b.last {
		return
	}
	b.last = value
	if value < "9999-12-31" && strings.HasSuffix(value, "T00:00:00") {
		if t, err := time.Parse(pivotDateLayout, value); err == nil {
			b.last = t.AddDate(0, 0, 1).Format(pivotDateLayout)
		}
	}
}

func calendarDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
func daysApart(a, b time.Time) int {
	return int((calendarDay(b).Unix() - calendarDay(a).Unix()) / 86400)
}
func (b dateBuckets) index(t time.Time) int {
	if t.Before(b.start) {
		return 0
	}
	if t.After(b.end) {
		return len(b.values) - 1
	}
	switch b.Unit {
	case "years":
		return 1 + t.Year() - b.start.Year()
	case "quarters":
		return 1 + (int(t.Month())-1)/3
	case "months":
		return int(t.Month())
	case "days":
		if b.Interval > 1 {
			return 1 + daysApart(b.start, t)/b.Interval
		}
		return time.Date(2000, t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).YearDay()
	case "hours":
		return t.Hour() + 1
	case "minutes":
		return t.Minute() + 1
	default:
		return t.Second() + 1
	}
}
func nativeDateLabel(t time.Time, english bool) string {
	if english {
		return fmt.Sprintf("%d/%d/%d", t.Month(), t.Day(), t.Year())
	}
	return fmt.Sprintf("%d/%d/%d", t.Year(), t.Month(), t.Day())
}
func (b dateBuckets) label(i int, english bool) string {
	switch b.Unit {
	case "years":
		year := strconv.Itoa(b.start.Year() + i)
		if english {
			return year
		}
		return year + "年"
	case "quarters":
		if english {
			return "Qtr" + strconv.Itoa(i+1)
		}
		return []string{"第一季", "第二季", "第三季", "第四季"}[i]
	case "months":
		if english {
			return time.Month(i + 1).String()[:3]
		}
		return strconv.Itoa(i+1) + "月"
	case "days":
		if b.Interval > 1 {
			a := calendarDay(b.start).AddDate(0, 0, i*b.Interval)
			end := a.AddDate(0, 0, b.Interval-1)
			if end.After(b.end) {
				end = b.end
			}
			return nativeDateLabel(a, english) + " - " + nativeDateLabel(end, english)
		}
		t := time.Date(2000, 1, i+1, 0, 0, 0, 0, time.UTC)
		if english {
			return t.Format("2-Jan")
		}
		return fmt.Sprintf("%d月%d日", t.Month(), t.Day())
	case "hours":
		if english {
			return fmt.Sprintf("%02d", i)
		}
		return fmt.Sprintf("%d时", i)
	case "minutes":
		if english {
			return fmt.Sprintf(":%02d", i)
		}
		return fmt.Sprintf("%d分", i)
	default:
		if english {
			return fmt.Sprintf(":%02d", i)
		}
		return fmt.Sprintf("%d秒", i)
	}
}
func (g pivotDateLevel) buckets(minimum, maximum string, english bool) (dateBuckets, error) {
	b := dateBuckets{cacheDateRange: g.rule}
	if pivotOn(b.AutoStart) {
		b.Start = minimum
	}
	if pivotOn(b.AutoEnd) {
		b.End = maximum
	}
	var err error
	b.start, err = editdoc.ISODateTime(b.Start)
	if err != nil {
		return b, errors.New("日期分组缺少有效起始日期")
	}
	b.end, err = editdoc.ISODateTime(b.End)
	if err != nil || b.end.Before(b.start) {
		return b, errors.New("日期分组结束日期早于起始日期")
	}
	b.Start, b.End = b.start.Format(pivotDateLayout), b.end.Format(pivotDateLayout)
	count := map[string]int{"seconds": 60, "minutes": 60, "hours": 24, "days": 366, "months": 12, "quarters": 4, "years": b.end.Year() - b.start.Year() + 1}[b.Unit]
	if b.Unit == "days" && b.Interval > 1 {
		count = 1 + daysApart(b.start, b.end)/b.Interval
	}
	if count < 1 || count > formats.EditCalcSourceCells {
		return b, ErrCalcCapacity
	}
	b.values = append(b.values, pivotValue{"s", "<" + nativeDateLabel(b.start, english)})
	for i := 0; i < count; i++ {
		b.values = append(b.values, pivotValue{"s", b.label(i, english)})
	}
	b.values = append(b.values, pivotValue{"s", ">" + nativeDateLabel(b.end, english)})
	// Preserve the producing Excel locale for existing calendar captions.
	if len(g.old) == len(b.values) && (b.Unit != "years" && !(b.Unit == "days" && b.Interval > 1) || b.Start == g.rule.Start && b.End == g.rule.End) {
		for i := 1; i < len(b.values)-1; i++ {
			b.values[i].raw = g.old[i].V
		}
	}
	if len(g.old) >= 2 {
		if b.Start == g.rule.Start {
			b.values[0].raw = g.old[0].V
		}
		if b.End == g.rule.End {
			b.values[len(b.values)-1].raw = g.old[len(g.old)-1].V
		}
	}
	return b, nil
}
func (g pivotDateLevel) xml(b dateBuckets) string {
	var out strings.Builder
	fmt.Fprintf(&out, `<fieldGroup xmlns="%s" base="%d"><rangePr groupBy="%s" startDate="%s" endDate="%s"`, mainNS, g.base, b.Unit, b.Start, b.End)
	if !pivotOn(b.AutoStart) {
		out.WriteString(` autoStart="0"`)
	}
	if !pivotOn(b.AutoEnd) {
		out.WriteString(` autoEnd="0"`)
	}
	if b.Interval > 1 {
		fmt.Fprintf(&out, ` groupInterval="%d"`, b.Interval)
	}
	fmt.Fprintf(&out, `/><groupItems count="%d">`, len(b.values))
	for _, v := range b.values {
		v.write(&out)
	}
	out.WriteString("</groupItems></fieldGroup>")
	return out.String()
}
