package reconcile

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/workbook"
)

func valueKind(v Value) string {
	if !v.Exists {
		return "missing"
	}
	switch v.Type {
	case "s", "str", "inlineStr":
		return "text"
	case "", "n":
		if v.Raw == "" {
			return "blank"
		}
		return "number"
	case "b":
		return "boolean"
	case "d":
		return "date"
	case "e":
		return "error"
	default:
		return "unknown"
	}
}

func exactNumber(v Value, explicit bool) (*big.Rat, bool) {
	kind := valueKind(v)
	if kind != "number" && (!explicit || kind != "text") {
		return nil, false
	}
	text := strings.TrimSpace(v.Raw)
	percent := false
	if explicit && kind == "text" && strings.HasSuffix(text, "%") {
		text, percent = strings.TrimSpace(strings.TrimSuffix(text, "%")), true
	}
	var number *big.Rat
	var ok bool
	if explicit && kind == "text" {
		number, ok = analysis.AccountingDecimal(text)
	} else {
		number, ok = analysis.Decimal(text)
	}
	if ok && percent {
		number.Quo(number, big.NewRat(100, 1))
	}
	return number, ok
}

func normalize(v Value, pair Pair) (string, string, *big.Rat, string) {
	if v.MissingCache {
		return "", "", nil, "公式缺少缓存值"
	}
	if v.Uncertain != "" {
		return "", "", nil, v.Uncertain
	}
	if v.TypeMissing && pair.Mode == "" {
		return "", "", nil, "此来源未保留原始值类型，请显式选择按文本或数值核对"
	}
	kind := valueKind(v)
	if kind == "error" || kind == "unknown" {
		return "", "", nil, "源单元格为错误值或未支持的类型"
	}
	if pair.Mode == "number" || pair.Mode == "" && kind == "number" {
		number, ok := exactNumber(v, pair.Mode == "number")
		if !ok {
			return "", "", nil, "无法按精确数值核对"
		}
		return "number", number.RatString(), number, ""
	}
	text := v.Raw
	if pair.Mode == "text" {
		kind = "text"
		if v.Display != "" {
			text = v.Display
		}
	}
	if kind == "boolean" {
		if text != "0" && text != "1" {
			return "", "", nil, "布尔源值无效"
		}
	}
	if kind == "text" {
		if pair.Trim {
			text = strings.TrimSpace(text)
		}
		if pair.IgnoreCase {
			text = workbook.FoldComparisonText(text)
		}
	}
	return kind, text, nil, ""
}

func recordKey(record Record, keys []Pair, right bool) (string, string) {
	var out strings.Builder
	out.WriteByte(1)
	var raw [10]byte
	for _, pair := range keys {
		col := pair.Left
		if right {
			col = pair.Right
		}
		value := record.Values[col]
		if value.MissingCache || value.Uncertain != "" {
			_, _, _, reason := normalize(value, pair)
			return "", reason
		}
		if !value.Exists || strings.TrimSpace(value.Raw) == "" {
			return "", "主键为空"
		}
		kind, text, _, reason := normalize(value, pair)
		if reason != "" {
			return "", reason
		}
		if len(kind)+len(text)+out.Len()+20 > MaxKeyBytes {
			return "", "主键超过1MiB核对限制"
		}
		out.WriteString(kind)
		out.WriteByte(0)
		n := binary.PutUvarint(raw[:], uint64(len(text)))
		out.Write(raw[:n])
		out.WriteString(text)
	}
	return out.String(), ""
}

func compareValues(left, right Value, pair Pair, position int) Comparison {
	leftKind, a, an, ae := normalize(left, pair)
	rightKind, b, bn, be := normalize(right, pair)
	comparison := Comparison{Pair: position}
	if ae != "" || be != "" {
		var reasons []string
		if ae != "" {
			reasons = append(reasons, "左侧："+ae)
		}
		if be != "" {
			reasons = append(reasons, "右侧："+be)
		}
		comparison.Reason = strings.Join(reasons, "；")
		return comparison
	}
	comparison.Equal = leftKind == rightKind && a == b
	if an != nil && bn != nil {
		comparison.Delta = analysis.ExactDecimalString(new(big.Rat).Sub(an, bn))
	}
	return comparison
}

func Validate(query Query, left, right Source) error {
	if len(query.Keys) == 0 || len(query.Keys) > MaxKeys || len(query.Fields) > MaxFields {
		return errors.New("请选择1至4组主键，最多8组比较字段")
	}
	for _, source := range []Source{left, right} {
		if source.Scan == nil || source.Read == nil || source.SourceRows < 0 || source.Info.Total < 0 || source.Info.Total > source.SourceRows {
			return errors.New("核对来源快照无效")
		}
	}
	for _, pairs := range [][]Pair{query.Keys, query.Fields} {
		seenLeft, seenRight := map[int]bool{}, map[int]bool{}
		for _, pair := range pairs {
			if pair.Mode != "" && pair.Mode != "text" && pair.Mode != "number" {
				return errors.New("字段核对方式无效")
			}
			if seenLeft[pair.Left] || seenRight[pair.Right] {
				return errors.New("同组主键或比较字段不能重复")
			}
			for side, column := range []int{pair.Left, pair.Right} {
				fields := left.Info.Fields
				if side == 1 {
					fields = right.Info.Fields
				}
				found := false
				for _, field := range fields {
					if column == field.Column && column > 0 {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("核对字段%d不存在，请重新选择", column)
				}
			}
			seenLeft[pair.Left], seenRight[pair.Right] = true, true
		}
	}
	return nil
}
