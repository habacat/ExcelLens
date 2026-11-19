package editfile

import (
	"math"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
)

type pivotAggregate struct {
	sum, min, max, product, mean, m2 float64
	numbers, count                   int
	countAdjustment                  float64
	err                              string
}

func (a *pivotAggregate) add(v pivotValue) {
	if v.kind != "m" {
		a.count++
	}
	if v.kind == "e" {
		a.err = v.raw
	}
	if v.kind != "n" {
		return
	}
	n, _ := strconv.ParseFloat(v.raw, 64)
	if a.numbers == 0 {
		a.min, a.max, a.product = n, n, 1
	} else {
		a.min = math.Min(a.min, n)
		a.max = math.Max(a.max, n)
	}
	a.numbers++
	a.sum += n
	a.product *= n
	delta := n - a.mean
	a.mean += delta / float64(a.numbers)
	a.m2 += delta * (n - a.mean)
}
func (a *pivotAggregate) addMeasure(value pivotValue, calculated bool) {
	a.add(value)
	if calculated && value.kind == "n" {
		n, _ := strconv.ParseFloat(value.raw, 64)
		a.countAdjustment += n - 1
	}
}

func (a pivotAggregate) value(method string) editdoc.Value {
	if a.err != "" && method != "count" && method != "countA" && method != "countNums" {
		return editdoc.Value{Kind: "error", Raw: a.err}
	}
	n, valid := a.statistic(method)
	if !valid {
		return editdoc.Value{Kind: "error", Raw: "#DIV/0!"}
	}
	switch method {
	case "count", "countA":
		n = float64(a.count) + a.countAdjustment
	case "average", "avg":
		if a.numbers == 0 {
			return editdoc.Value{Kind: "error", Raw: "#DIV/0!"}
		}
		n /= float64(a.numbers)
	case "min":
		n = a.min
	case "max":
		n = a.max
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return editdoc.Value{Kind: "error", Raw: "#NUM!"}
	}
	return editdoc.Value{Kind: "number", Raw: strconv.FormatFloat(n, 'g', -1, 64)}
}

// Extra statistics use the original records, including at subtotal crossings.
// The numerically stable second moment supports both sample and population totals.
func (a pivotAggregate) statistic(method string) (float64, bool) {
	switch method {
	case "product":
		return a.product, true
	case "countNums":
		return float64(a.numbers), true
	case "stdDev", "stdDevP", "var", "varP":
		n := a.numbers
		if method == "stdDev" || method == "var" {
			n--
		}
		if n <= 0 {
			return 0, false
		}
		v := a.m2 / float64(n)
		if strings.HasPrefix(method, "stdDev") {
			v = math.Sqrt(v)
		}
		return v, true
	}
	return a.sum, true
}
