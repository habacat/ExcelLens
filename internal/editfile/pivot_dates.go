package editfile

import (
	"math"
	"strconv"
	"time"

	"excellens/internal/editdoc"
)

const pivotDateLayout = "2006-01-02T15:04:05"

func pivotEpoch(date1904 bool) time.Time {
	if date1904 {
		return time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	// Excel's pivot cache uses the Automation epoch, including for serials
	// before March 1900. The worksheet's fictitious leap day remains serial 60.
	return time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
}

func pivotDateSerial(raw string, date1904 bool) (float64, error) {
	t, err := editdoc.ISODateTime(raw)
	if err != nil {
		return 0, err
	}
	return float64(t.Unix()-pivotEpoch(date1904).Unix()) / float64(secondsPerDay), nil
}

func (b *CalculationBook) pivotDate(serial float64) (pivotValue, error) {
	if serial < 0 || serial >= float64(maxExcelDate(b.date1904)+1) {
		return pivotValue{"n", strconv.FormatFloat(serial, 'g', -1, 64)}, nil
	}
	// Native cache dates are rounded to whole seconds before grouping.
	instant := time.Unix(pivotEpoch(b.date1904).Unix()+int64(math.Round(serial*float64(secondsPerDay))), 0).UTC()
	if instant.Year() > 9999 {
		return pivotValue{}, ErrXLSX
	}
	return pivotValue{"d", instant.Format(pivotDateLayout)}, nil
}
