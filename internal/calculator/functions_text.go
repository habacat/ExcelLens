package calculator

import (
	"errors"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"
)

func registerTextFunctions(add functionRegistrar) {
	add("PROPER,CLEAN,CODE,UNICODE,CHAR,UNICHAR", "值", 1, 1, textFunction)
	add("REPLACE", "文本, 起点, 字符数, 新文本", 4, 4, textFunction)
	add("REPT", "文本, 次数", 2, 2, textFunction)
	add("NUMBERVALUE", "文本, [小数分隔符], [分组分隔符]", 1, 3, textFunction)
	add("TEXTBEFORE,TEXTAFTER", "文本, 分隔符, [第几次], [忽略大小写], [匹配末尾], [未找到时]", 2, 6, textFunction)
	add("FIXED", "数值, [小数位数], [不分组]", 1, 3, textFunction)
}
func textFunction(e *formulaEval, n *formulaNode) (FormulaValue, error) {
	args := n
	if (n.op == "TEXTBEFORE" || n.op == "TEXTAFTER") && len(n.args) == 6 {
		args = &formulaNode{op: n.op, args: n.args[:5]}
	}
	vals, err := evaluatedArgs(e, args)
	if err != nil {
		return FormulaValue{}, err
	}
	text, err := formulaText(vals[0])
	if err != nil {
		return FormulaValue{}, err
	}
	switch n.op {
	case "PROPER":
		var b strings.Builder
		start := true
		for _, r := range text {
			if unicode.IsLetter(r) {
				if start {
					r = unicode.ToUpper(r)
				} else {
					r = unicode.ToLower(r)
				}
				start = false
			} else {
				start = true
			}
			b.WriteRune(r)
		}
		return TextValue(b.String()), nil
	case "CLEAN":
		return TextValue(strings.Map(func(r rune) rune {
			if r < 32 {
				return -1
			}
			return r
		}, text)), nil
	case "CODE", "UNICODE":
		if text == "" {
			return FormulaValue{}, errors.New("#VALUE! 文本为空")
		}
		r, _ := utf8.DecodeRuneInString(text)
		if n.op == "CODE" && r > 255 {
			return FormulaValue{}, errors.New("#VALUE! CODE 支持单字节字符；Unicode请用UNICODE")
		}
		return integerResult(int64(r))
	case "CHAR", "UNICHAR":
		i, err := formulaInteger(vals[0])
		max := int64(0x10ffff)
		if n.op == "CHAR" {
			max = 255
		}
		if err != nil || i < 1 || i > max || i >= 0xd800 && i <= 0xdfff {
			return FormulaValue{}, errors.New("#VALUE! 字符编码无效")
		}
		return TextValue(string(rune(i))), nil
	case "REPLACE":
		start, err := formulaInteger(vals[1])
		if err != nil {
			return FormulaValue{}, err
		}
		count, err := formulaInteger(vals[2])
		if err != nil || start < 1 || count < 0 {
			return FormulaValue{}, errors.New("#VALUE! 替换位置无效")
		}
		replacement, err := formulaText(vals[3])
		if err != nil {
			return FormulaValue{}, err
		}
		runes := []rune(text)
		a := min(len(runes), int(start-1))
		b := min(len(runes), a+int(min(count, int64(len(runes)))))
		result := string(runes[:a]) + replacement + string(runes[b:])
		if len(result) > maxFormulaText {
			return FormulaValue{}, errors.New("#NUM! 文本结果过长")
		}
		return TextValue(result), nil
	case "REPT":
		count, err := formulaInteger(vals[1])
		if err != nil || count < 0 {
			return FormulaValue{}, errors.New("#VALUE! 重复次数无效")
		}
		if count > maxFormulaText || int64(len(text))*count > maxFormulaText {
			return FormulaValue{}, errors.New("#NUM! 文本结果过长")
		}
		return TextValue(strings.Repeat(text, int(count))), nil
	case "NUMBERVALUE":
		decimal, group := ".", ","
		if len(vals) > 1 {
			decimal, err = formulaText(vals[1])
			if err != nil {
				return FormulaValue{}, err
			}
		}
		if len(vals) > 2 {
			group, err = formulaText(vals[2])
			if err != nil {
				return FormulaValue{}, err
			}
		}
		if decimal == group || utf8.RuneCountInString(decimal) != 1 || utf8.RuneCountInString(group) > 1 {
			return FormulaValue{}, errors.New("#VALUE! 分隔符无效")
		}
		text = strings.ReplaceAll(text, " ", "")
		if group != "" {
			text = strings.ReplaceAll(text, group, "")
		}
		text = strings.ReplaceAll(text, decimal, ".")
		if text == "" {
			return integerResult(0)
		}
		number, err := formulaNumber(TextValue(text))
		return NumberValue(number), err
	case "FIXED":
		number, err := formulaNumber(vals[0])
		if err != nil {
			return FormulaValue{}, err
		}
		places := int64(2)
		if len(vals) > 1 {
			places, err = formulaInteger(vals[1])
		}
		if err != nil || places < -20 || places > 20 {
			return FormulaValue{}, errors.New("#NUM! 小数位数超限")
		}
		noGroup := false
		if len(vals) > 2 {
			noGroup, err = formulaBool(vals[2])
			if err != nil {
				return FormulaValue{}, err
			}
		}
		if places < 0 {
			factor := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(-places), nil))
			scaled := new(big.Rat).Quo(number, factor)
			rounded, err := Canonical(Format(scaled, 0, false).Copy)
			if err != nil {
				return FormulaValue{}, err
			}
			number = rounded.Mul(rounded, factor)
			places = 0
		}
		mask := "0"
		if !noGroup {
			mask = "#,##0"
		}
		if places > 0 {
			mask += "." + strings.Repeat("0", int(places))
		}
		result, err := formatFormulaText(number, mask)
		return TextValue(result), err
	case "TEXTBEFORE", "TEXTAFTER":
		delimiter, err := formulaText(vals[1])
		if err != nil {
			return FormulaValue{}, err
		}
		instance := int64(1)
		if len(vals) > 2 && vals[2].Kind != "blank" {
			instance, err = formulaInteger(vals[2])
		}
		if err != nil || instance == 0 {
			return FormulaValue{}, errors.New("#VALUE! 实例序号无效")
		}
		ignore := false
		if len(vals) > 3 {
			mode, err := formulaInteger(vals[3])
			if err != nil || mode < 0 || mode > 1 {
				return FormulaValue{}, errors.New("#VALUE! 匹配方式无效")
			}
			ignore = mode == 1
		}
		matchEnd := false
		if len(vals) > 4 {
			mode, err := formulaInteger(vals[4])
			if err != nil || mode < 0 || mode > 1 {
				return FormulaValue{}, errors.New("#VALUE! 匹配末尾参数无效")
			}
			matchEnd = mode == 1
		}
		hay, needle := text, delimiter
		if ignore {
			hay = strings.ToUpper(hay)
			needle = strings.ToUpper(needle)
		}
		positions := []int{}
		if delimiter != "" {
			for from := 0; from <= len(hay); {
				at := strings.Index(hay[from:], needle)
				if at < 0 {
					break
				}
				positions = append(positions, from+at)
				from += at + len(needle)
			}
		}
		index := int(instance - 1)
		if instance < 0 {
			index = len(positions) + int(instance)
		}
		if index < 0 || index >= len(positions) {
			if delimiter == "" {
				if n.op == "TEXTBEFORE" && instance > 0 || n.op == "TEXTAFTER" && instance < 0 {
					return TextValue(""), nil
				}
				return TextValue(text), nil
			}
			if matchEnd && int64(len(positions))+1 == int64(absInt(int(instance))) {
				if n.op == "TEXTBEFORE" && instance > 0 || n.op == "TEXTAFTER" && instance < 0 {
					return TextValue(text), nil
				}
				return TextValue(""), nil
			}
			if len(n.args) > 5 {
				return e.eval(n.args[5])
			}
			return FormulaValue{}, errors.New("#N/A 未找到分隔符")
		}
		at := positions[index]
		if n.op == "TEXTBEFORE" {
			return TextValue(text[:at]), nil
		}
		return TextValue(text[at+len(delimiter):]), nil
	}
	return FormulaValue{}, errors.New("#NAME? 未注册文本函数")
}
