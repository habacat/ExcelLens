package calculator

import (
	"errors"
	"strings"
)

func registerLogicFunctions(add functionRegistrar) {
	add("IFS", "条件1, 值1, …", 2, 128, logicFunction)
	add("SWITCH", "表达式, 匹配值1, 返回值1, …", 3, 128, logicFunction)
	add("CHOOSE", "序号, 值1, …", 2, 128, logicFunction)
	add("IFNA", "表达式, 未找到时", 2, 2, logicFunction)
	add("ISNA,ISERR,ISLOGICAL,ISNONTEXT,ISEVEN,ISODD,N,T,TYPE,ERROR.TYPE", "值", 1, 1, logicFunction)
	add("XOR", "条件1, …", 1, 128, logicFunction)
	add("NA", "", 0, 0, logicFunction)
	add("LET", "名称1, 值1, …, 计算表达式", 3, 127, logicFunction)
	add("ISREF,ISNUMBER,ISTEXT,ISBLANK,ISERROR", "值", 1, 1, logicFunction)
}
func logicFunction(e *formulaEval, n *formulaNode) (FormulaValue, error) {
	switch n.op {
	case "LET":
		if len(n.args)%2 != 1 {
			return FormulaValue{}, errors.New("#VALUE! LET 名称和值必须成对")
		}
		previous := e.names
		scope := make(map[string]FormulaValue, len(previous)+len(n.args)/2)
		for k, v := range previous {
			scope[k] = v
		}
		e.names = scope
		defer func() { e.names = previous }()
		for i := 0; i < len(n.args)-1; i += 2 {
			name := n.args[i]
			if name.kind != "name" || name.op == "R" || name.op == "C" {
				return FormulaValue{}, errors.New("#NAME? LET 变量名不能是单元格地址")
			}
			value, err := e.eval(n.args[i+1])
			if err != nil {
				return FormulaValue{}, err
			}
			scope[name.op] = value
		}
		return e.eval(n.args[len(n.args)-1])
	case "ISREF":
		if n.args[0].kind != "ref" {
			return BoolValue(false), nil
		}
		_, err := e.get(n.args[0].ref)
		if e.ctx.Err() != nil {
			return FormulaValue{}, e.ctx.Err()
		}
		return BoolValue(err == nil), nil
	case "ISNUMBER", "ISTEXT", "ISBLANK", "ISERROR", "ISLOGICAL", "ISNONTEXT", "TYPE":
		value, err := e.eval(n.args[0])
		if e.ctx.Err() != nil {
			return FormulaValue{}, e.ctx.Err()
		}
		if n.op == "ISERROR" {
			return BoolValue(err != nil), nil
		}
		if n.op == "TYPE" {
			if err != nil {
				return integerResult(16)
			}
			switch value.Kind {
			case "text":
				return integerResult(2)
			case "boolean":
				return integerResult(4)
			case "range":
				return integerResult(64)
			}
			return integerResult(1)
		}
		if n.op == "ISNONTEXT" {
			return BoolValue(err != nil || value.Kind != "text"), nil
		}
		kind := map[string]string{"ISNUMBER": "number", "ISTEXT": "text", "ISBLANK": "blank", "ISLOGICAL": "boolean"}[n.op]
		return BoolValue(err == nil && value.Kind == kind), nil
	case "NA":
		return FormulaValue{}, errors.New("#N/A")
	case "IFS":
		if len(n.args)%2 != 0 {
			return FormulaValue{}, errors.New("#VALUE! IFS 条件与返回值必须成对")
		}
		for i := 0; i < len(n.args); i += 2 {
			v, err := e.eval(n.args[i])
			if err != nil {
				return FormulaValue{}, err
			}
			yes, err := formulaBool(v)
			if err != nil {
				return FormulaValue{}, err
			}
			if yes {
				return e.eval(n.args[i+1])
			}
		}
		return FormulaValue{}, errors.New("#N/A 没有符合条件的分支")
	case "SWITCH":
		x, err := e.eval(n.args[0])
		if err != nil {
			return FormulaValue{}, err
		}
		for i := 1; i+1 < len(n.args); i += 2 {
			v, err := e.eval(n.args[i])
			if err != nil {
				return FormulaValue{}, err
			}
			cmp, err := compareFormulaValues(x, v)
			if err != nil {
				return FormulaValue{}, err
			}
			if cmp == 0 {
				return e.eval(n.args[i+1])
			}
		}
		if len(n.args)%2 == 0 {
			return e.eval(n.args[len(n.args)-1])
		}
		return FormulaValue{}, errors.New("#N/A 没有匹配分支")
	case "CHOOSE":
		v, err := e.eval(n.args[0])
		if err != nil {
			return FormulaValue{}, err
		}
		index, err := formulaInteger(v)
		if err != nil || index < 1 || index >= int64(len(n.args)) {
			return FormulaValue{}, errors.New("#VALUE! CHOOSE 序号无效")
		}
		return e.eval(n.args[index])
	case "IFNA", "ISNA", "ISERR", "ERROR.TYPE":
		v, err := e.eval(n.args[0])
		na := err != nil && strings.HasPrefix(err.Error(), "#N/A")
		if e.ctx.Err() != nil {
			return FormulaValue{}, e.ctx.Err()
		}
		if n.op == "IFNA" {
			if na {
				return e.eval(n.args[1])
			}
			return v, err
		}
		if n.op == "ISNA" {
			return BoolValue(na), nil
		}
		if n.op == "ISERR" {
			return BoolValue(err != nil && !na), nil
		}
		if err == nil {
			return FormulaValue{}, errors.New("#N/A 不是错误值")
		}
		for i, code := range []string{"#NULL!", "#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A", "#GETTING_DATA"} {
			if strings.HasPrefix(err.Error(), code) {
				return integerResult(int64(i + 1))
			}
		}
		return integerResult(3)
	}
	vals, err := evaluatedArgs(e, n)
	if err != nil {
		return FormulaValue{}, err
	}
	v := vals[0]
	switch n.op {
	case "ISLOGICAL":
		return BoolValue(v.Kind == "boolean"), nil
	case "ISNONTEXT":
		return BoolValue(v.Kind != "text"), nil
	case "ISEVEN", "ISODD":
		number, err := formulaNumber(v)
		if err != nil {
			return FormulaValue{}, err
		}
		odd := truncInteger(number).Bit(0) == 1
		return BoolValue(odd == (n.op == "ISODD")), nil
	case "N":
		switch v.Kind {
		case "number", "boolean", "blank":
			number, err := formulaNumber(v)
			return NumberValue(number), err
		}
		return integerResult(0)
	case "T":
		if v.Kind == "text" {
			return v, nil
		}
		return TextValue(""), nil
	case "TYPE":
		kind := int64(1)
		switch v.Kind {
		case "text":
			kind = 2
		case "boolean":
			kind = 4
		case "error":
			kind = 16
		case "range":
			kind = 64
		}
		return integerResult(kind)
	case "XOR":
		flat, err := e.flattenFormulaValues(vals)
		if err != nil {
			return FormulaValue{}, err
		}
		answer := false
		for _, v := range flat {
			if v.Kind == "blank" {
				continue
			}
			yes, err := formulaBool(v)
			if err != nil {
				return FormulaValue{}, err
			}
			answer = answer != yes
		}
		return BoolValue(answer), nil
	}
	return FormulaValue{}, errors.New("#NAME? 未注册逻辑函数")
}
