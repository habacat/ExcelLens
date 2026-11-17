package calculator

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"excellens/internal/valueconv"
)

type dateSystemKey struct{}

func WithDateSystem(ctx context.Context, date1904 bool) context.Context {
	return context.WithValue(ctx, dateSystemKey{}, date1904)
}
func date1904FromContext(ctx context.Context) bool {
	v, _ := ctx.Value(dateSystemKey{}).(bool)
	return v
}
func registerDateFunctions(add functionRegistrar) {
	add("DATE", "年, 月, 日", 3, 3, dateFunction)
	add("TIME", "时, 分, 秒", 3, 3, dateFunction)
	add("TODAY,NOW", "", 0, 0, dateFunction)
	add("YEAR,MONTH,DAY,HOUR,MINUTE,SECOND,DATEVALUE,TIMEVALUE,ISOWEEKNUM", "日期或时间", 1, 1, dateFunction)
	add("WEEKDAY,WEEKNUM", "日期, [类型]", 1, 2, dateFunction)
	add("EDATE,EOMONTH,DAYS", "日期, 参数", 2, 2, dateFunction)
	add("DATEDIF", "起始日期, 结束日期, 单位", 3, 3, dateFunction)
	add("DAYS360,YEARFRAC", "起始日期, 结束日期, [方法]", 2, 3, dateFunction)
	add("NETWORKDAYS,WORKDAY", "起始日期, 结束日期或天数, [节假日]", 2, 3, dateFunction)
	add("NETWORKDAYS.INTL,WORKDAY.INTL", "起始日期, 结束日期或天数, [周末], [节假日]", 2, 4, dateFunction)
}
func (e *formulaEval) dateSerial(t time.Time) *big.Rat {
	y, m, d := t.Date()
	t = time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	base := time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	if e.date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	days := (t.Unix() - base.Unix()) / 86400
	if !e.date1904 && !t.Before(time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC)) {
		days++
	}
	return big.NewRat(days, 1)
}
func (e *formulaEval) serialTime(v FormulaValue) (time.Time, int64, error) {
	if v.Kind == "text" {
		s := strings.TrimSpace(v.Text)
		parsed, parseErr := valueconv.Parse(s, "auto")
		t := parsed.Time
		if parseErr != nil {
			// Retain the pre-existing explicit formula support for RFC3339.
			// Suggestions and analysis conversions never strip timezone data.
			t, parseErr = time.Parse(time.RFC3339, s)
		}
		if parseErr == nil {
			if t.Year() == 0 {
				t = time.Date(1899, 12, 31, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
			}
			n := e.dateSerial(t)
			day := n.Num().Int64()
			return t, day, nil
		}
	}
	n, err := formulaNumber(v)
	if err != nil {
		return time.Time{}, 0, errors.New("#VALUE! 无效日期或时间")
	}
	whole := floorInteger(n)
	if !whole.IsInt64() || whole.Int64() < 0 || whole.Int64() > 2958465 {
		return time.Time{}, 0, errors.New("#NUM! 日期序号越界")
	}
	days := whole.Int64()
	adjusted := days
	base := time.Date(1899, 12, 31, 0, 0, 0, 0, time.UTC)
	if e.date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else if days >= 60 {
		adjusted--
	}
	fraction := new(big.Rat).Sub(n, new(big.Rat).SetInt(whole))
	// Calendar extraction must not round 23:59:59.xxx into the next day.
	// Convert the exact fractional day to nanoseconds, truncating only below
	// time.Time's resolution; every value below 1 stays inside that day.
	nanos := new(big.Rat).Mul(fraction, big.NewRat(int64(24*time.Hour), 1))
	offset := new(big.Int).Quo(nanos.Num(), nanos.Denom()).Int64()
	t := base.AddDate(0, 0, int(adjusted)).Add(time.Duration(offset))
	return t, days, nil
}
func monthShift(t time.Time, months int, end bool) time.Time {
	first := time.Date(t.Year(), t.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1)
	day := t.Day()
	if end || day > last.Day() {
		day = last.Day()
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}
func dateFunction(e *formulaEval, n *formulaNode) (FormulaValue, error) {
	vals, err := evaluatedArgs(e, n)
	if err != nil {
		return FormulaValue{}, err
	}
	integer := func(i int, def int64) (int64, error) {
		if i >= len(vals) {
			return def, nil
		}
		return formulaInteger(vals[i])
	}
	switch n.op {
	case "TODAY", "NOW":
		now := e.now
		serial := e.dateSerial(now)
		if n.op == "NOW" {
			ns := int64(now.Hour()*3600+now.Minute()*60+now.Second())*1e9 + int64(now.Nanosecond())
			serial.Add(serial, big.NewRat(ns, 86400*1e9))
		}
		return numberResult(serial)
	case "DATE":
		y, err := integer(0, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		m, err := integer(1, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		d, err := integer(2, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		if y >= 0 && y < 1900 {
			y += 1900
		}
		if y < 1900 || y > 9999 || m < -120000 || m > 120000 || d < -1000000 || d > 1000000 {
			return FormulaValue{}, errors.New("#NUM! 日期参数越界")
		}
		if !e.date1904 && y == 1900 && m == 2 && d == 29 {
			return integerResult(60)
		}
		t := time.Date(int(y), time.Month(m), int(d), 0, 0, 0, 0, time.UTC)
		if t.Year() < 1900 || t.Year() > 9999 {
			return FormulaValue{}, errors.New("#NUM! 日期越界")
		}
		return numberResult(e.dateSerial(t))
	case "TIME":
		h, err := integer(0, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		m, err := integer(1, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		s, err := integer(2, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		if h < 0 || m < 0 || s < 0 || h > 32767 || m > 32767 || s > 32767 {
			return FormulaValue{}, errors.New("#NUM! 时间参数越界")
		}
		return numberResult(big.NewRat((h*3600+m*60+s)%86400, 86400))
	}
	t, serial, err := e.serialTime(vals[0])
	if err != nil {
		return FormulaValue{}, err
	}
	fake := !e.date1904 && serial == 60
	switch n.op {
	case "DATEVALUE":
		if vals[0].Kind != "text" || !strings.ContainsAny(vals[0].Text, "-/年月日.") {
			return FormulaValue{}, errors.New("#VALUE! DATEVALUE 需要日期文本")
		}
		return numberResult(e.dateSerial(t))
	case "TIMEVALUE":
		return numberResult(big.NewRat(int64(t.Hour()*3600+t.Minute()*60+t.Second())*1e9+int64(t.Nanosecond()), 86400*1e9))
	case "YEAR":
		if !e.date1904 && serial == 0 {
			return integerResult(1900)
		}
		return integerResult(int64(t.Year()))
	case "MONTH":
		if !e.date1904 && serial == 0 {
			return integerResult(1)
		}
		return integerResult(int64(t.Month()))
	case "DAY":
		if !e.date1904 && serial == 0 {
			return integerResult(0)
		}
		if fake {
			return integerResult(29)
		}
		return integerResult(int64(t.Day()))
	case "HOUR":
		return integerResult(int64(t.Hour()))
	case "MINUTE":
		return integerResult(int64(t.Minute()))
	case "SECOND":
		return integerResult(int64(t.Second()))
	case "ISOWEEKNUM":
		_, week := t.ISOWeek()
		return integerResult(int64(week))
	case "WEEKDAY":
		kind, err := integer(1, 1)
		if err != nil {
			return FormulaValue{}, err
		}
		weekday := int(t.Weekday())
		if !e.date1904 && serial < 61 {
			weekday = (weekday + 6) % 7
		}
		if kind == 1 {
			return integerResult(int64(weekday + 1))
		}
		if kind == 2 {
			return integerResult(int64((weekday+6)%7 + 1))
		}
		if kind == 3 {
			return integerResult(int64((weekday + 6) % 7))
		}
		if kind >= 11 && kind <= 17 {
			start := int(kind-10) % 7
			return integerResult(int64((weekday-start+7)%7 + 1))
		}
		return FormulaValue{}, errors.New("#NUM! WEEKDAY 类型无效")
	case "WEEKNUM":
		kind, err := integer(1, 1)
		if err != nil {
			return FormulaValue{}, err
		}
		if kind == 21 {
			_, week := t.ISOWeek()
			return integerResult(int64(week))
		}
		start := 0
		if kind == 2 {
			start = 1
		} else if kind >= 11 && kind <= 17 {
			start = int(kind-10) % 7
		} else if kind != 1 {
			return FormulaValue{}, errors.New("#NUM! WEEKNUM 类型无效")
		}
		jan := time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		offset := (int(jan.Weekday()) - start + 7) % 7
		return integerResult(int64((t.YearDay()-1+offset)/7 + 1))
	case "EDATE", "EOMONTH":
		months, err := integer(1, 0)
		if err != nil || months < -120000 || months > 120000 {
			return FormulaValue{}, errors.New("#NUM! 月份偏移越界")
		}
		result := monthShift(t, int(months), n.op == "EOMONTH")
		if result.Year() < 1900 || result.Year() > 9999 {
			return FormulaValue{}, errors.New("#NUM! 日期越界")
		}
		return numberResult(e.dateSerial(result))
	case "NETWORKDAYS", "WORKDAY", "NETWORKDAYS.INTL", "WORKDAY.INTL":
		return businessDateFunction(e, n.op, vals, t, serial)
	}
	end, endSerial, err := e.serialTime(vals[1])
	if err != nil {
		return FormulaValue{}, err
	}
	days := endSerial - serial
	switch n.op {
	case "DAYS":
		return integerResult(-days)
	case "DATEDIF":
		if days < 0 {
			return FormulaValue{}, errors.New("#NUM! 起始日期晚于结束日期")
		}
		unit, err := formulaText(vals[2])
		if err != nil {
			return FormulaValue{}, err
		}
		months := (end.Year()-t.Year())*12 + int(end.Month()-t.Month())
		if end.Day() < t.Day() {
			months--
		}
		switch strings.ToUpper(unit) {
		case "D":
			return integerResult(days)
		case "M":
			return integerResult(int64(months))
		case "Y":
			return integerResult(int64(months / 12))
		case "YM":
			return integerResult(int64(months % 12))
		case "MD":
			shift := monthShift(t, months, false)
			return integerResult((end.Unix() - shift.Unix()) / 86400)
		case "YD":
			start := time.Date(end.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			if start.After(end) {
				start = start.AddDate(-1, 0, 0)
			}
			return integerResult((end.Unix() - start.Unix()) / 86400)
		}
		return FormulaValue{}, errors.New("#NUM! DATEDIF 单位无效")
	case "DAYS360", "YEARFRAC":
		basis := int64(0)
		if len(vals) > 2 && vals[2].Kind != "blank" {
			if n.op == "DAYS360" {
				eu, err := formulaBool(vals[2])
				if err != nil {
					return FormulaValue{}, err
				}
				if eu {
					basis = 4
				}
			} else {
				basis, err = formulaInteger(vals[2])
				if err != nil {
					return FormulaValue{}, err
				}
			}
		}
		if basis < 0 || basis > 4 {
			return FormulaValue{}, errors.New("#NUM! YEARFRAC 基础无效")
		}
		numerator := days
		denominator := int64(360)
		if basis == 0 || basis == 4 {
			sign := int64(1)
			a, b := t, end
			if a.After(b) {
				a, b = b, a
				sign = -1
			}
			da, db := a.Day(), b.Day()
			lastA := a.AddDate(0, 0, 1).Month() != a.Month()
			lastB := b.AddDate(0, 0, 1).Month() != b.Month()
			if basis == 4 {
				da = min(da, 30)
				db = min(db, 30)
			} else {
				if da == 31 || a.Month() == 2 && lastA {
					da = 30
				}
				if db == 31 && da >= 30 || b.Month() == 2 && lastB && a.Month() == 2 && lastA {
					db = 30
				}
			}
			numerator = sign * int64((b.Year()-a.Year())*360+int(b.Month()-a.Month())*30+db-da)
		} else if basis == 3 {
			denominator = 365
		} else if basis == 1 {
			a, b := t, end
			if a.After(b) {
				a, b = b, a
			}
			if a.Year() == b.Year() {
				denominator = int64(time.Date(a.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC).Sub(time.Date(a.Year(), 1, 1, 0, 0, 0, 0, time.UTC)).Hours() / 24)
			} else if b.Year() == a.Year()+1 && (b.Month() < a.Month() || b.Month() == a.Month() && b.Day() <= a.Day()) {
				denominator = 365
				for year := a.Year(); year <= b.Year(); year++ {
					leapDay := time.Date(year, 2, 29, 0, 0, 0, 0, time.UTC)
					if leapDay.Month() == 2 && !leapDay.Before(a) && leapDay.Before(b) {
						denominator = 366
					}
				}
			} else {
				a, b := t.Year(), end.Year()
				if a > b {
					a, b = b, a
				}
				years := int64(b - a + 1)
				total := (time.Date(b+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix() - time.Date(a, 1, 1, 0, 0, 0, 0, time.UTC).Unix()) / 86400
				return numberResult(big.NewRat(days*years, total))
			}
		}
		if n.op == "DAYS360" {
			return integerResult(numerator)
		}
		return numberResult(big.NewRat(numerator, denominator))
	}
	return FormulaValue{}, errors.New("#NAME? 未注册日期函数")
}
func businessDateFunction(e *formulaEval, name string, vals []FormulaValue, start time.Time, startSerial int64) (FormulaValue, error) {
	weekend := [7]bool{true, false, false, false, false, false, true}
	holidayIndex := 2
	if strings.HasSuffix(name, ".INTL") {
		holidayIndex = 3
		if len(vals) > 2 && vals[2].Kind != "blank" {
			if vals[2].Kind == "text" {
				text := vals[2].Text
				if len(text) != 7 || text == "1111111" {
					return FormulaValue{}, errors.New("#VALUE! 周末字符串无效")
				}
				for i, c := range text {
					if c != '0' && c != '1' {
						return FormulaValue{}, errors.New("#VALUE! 周末字符串无效")
					}
					weekend[(i+1)%7] = c == '1'
				}
			} else {
				k, err := formulaInteger(vals[2])
				if err != nil {
					return FormulaValue{}, err
				}
				weekend = [7]bool{}
				if k >= 1 && k <= 7 {
					a := int((k + 5) % 7)
					weekend[a] = true
					weekend[(a+1)%7] = true
				} else if k >= 11 && k <= 17 {
					weekend[int(k-11)] = true
				} else {
					return FormulaValue{}, errors.New("#NUM! 周末类型无效")
				}
			}
		}
	}
	holidays := map[int64]bool{}
	if len(vals) > holidayIndex {
		if err := visitSparse(e, vals[holidayIndex], func(_ int, x FormulaValue) error {
			if x.Kind == "blank" {
				return nil
			}
			_, day, err := e.serialTime(x)
			if err != nil {
				return err
			}
			holidays[day] = true
			return nil
		}); err != nil {
			return FormulaValue{}, err
		}
	}
	working := func(t time.Time) bool {
		day := e.dateSerial(t).Num().Int64()
		return !weekend[int(t.Weekday())] && !holidays[day]
	}
	if strings.HasPrefix(name, "NETWORKDAYS") {
		_, endSerial, err := e.serialTime(vals[1])
		if err != nil {
			return FormulaValue{}, err
		}
		sign := int64(1)
		if endSerial < startSerial {
			startSerial, endSerial = endSerial, startSerial
			sign = -1
			t, _, err := e.serialTime(NumberValue(big.NewRat(startSerial, 1)))
			if err != nil {
				return FormulaValue{}, err
			}
			start = t
		}
		if endSerial-startSerial > 1000000 {
			return FormulaValue{}, errors.New("#NUM! 工作日区间过长")
		}
		count := int64(0)
		for i := int64(0); i <= endSerial-startSerial; i++ {
			if err := e.ctx.Err(); err != nil {
				return FormulaValue{}, err
			}
			if working(start.AddDate(0, 0, int(i))) {
				count++
			}
		}
		return integerResult(count * sign)
	}
	amount, err := formulaInteger(vals[1])
	if err != nil || amount < -100000 || amount > 100000 {
		return FormulaValue{}, errors.New("#NUM! 工作日偏移超限")
	}
	step := 1
	if amount < 0 {
		step = -1
		amount = -amount
	}
	t := start
	for amount > 0 {
		if err := e.ctx.Err(); err != nil {
			return FormulaValue{}, err
		}
		t = t.AddDate(0, 0, step)
		if working(t) {
			amount--
		}
	}
	return numberResult(e.dateSerial(t))
}

func formatDateMask(e *formulaEval, value FormulaValue, mask string) (string, error) {
	t, serial, err := e.serialTime(value)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	lower := strings.ToLower(mask)
	quoted := false
	for i := 0; i < len(mask); {
		if mask[i] == '"' {
			quoted = !quoted
			i++
			continue
		}
		if quoted {
			b.WriteByte(mask[i])
			i++
			continue
		}
		if mask[i] == '\\' && i+1 < len(mask) {
			b.WriteByte(mask[i+1])
			i += 2
			continue
		}
		ch := lower[i]
		if !strings.ContainsRune("ymdhs", rune(ch)) {
			b.WriteByte(mask[i])
			i++
			continue
		}
		j := i + 1
		for j < len(mask) && lower[j] == ch {
			j++
		}
		length := j - i
		value := 0
		switch ch {
		case 'y':
			value = t.Year()
			if length == 2 {
				value %= 100
			}
		case 'd':
			value = t.Day()
			if !e.date1904 && serial == 60 {
				value = 29
			}
			if length >= 3 {
				names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
				if length >= 4 {
					names = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
				}
				b.WriteString(names[t.Weekday()])
				i = j
				continue
			}
		case 'm':
			minutes := i > 0 && strings.Contains(lower[:i], "h") || j < len(mask) && strings.HasPrefix(lower[j:], ":s")
			if minutes {
				value = t.Minute()
			} else {
				value = int(t.Month())
				if length >= 3 {
					format := "Jan"
					if length >= 4 {
						format = "January"
					}
					b.WriteString(t.Format(format))
					i = j
					continue
				}
			}
		case 'h':
			value = t.Hour()
		case 's':
			value = t.Second()
		}
		text := strconv.Itoa(value)
		width := length
		if ch == 'y' && length > 2 {
			width = 4
		}
		if width <= 4 && len(text) < width {
			text = strings.Repeat("0", width-len(text)) + text
		}
		b.WriteString(text)
		i = j
	}
	if quoted {
		return "", errors.New("#VALUE! 日期格式引号未闭合")
	}
	return b.String(), nil
}
