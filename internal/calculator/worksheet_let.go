package calculator

import (
	"maps"
	"strings"
)

// LET introduces each binding after evaluating its value. Shadowed workbook
// names and an outer LET scope remain visible in earlier arguments.
func bindLetNames(n *formulaNode, locals map[string]bool) error {
	if n.kind == "name" {
		key := strings.TrimPrefix(n.op, "_XLPM.")
		if scalar, ok := locals[key]; ok {
			n.kind = "localName"
			n.op = key
			if scalar {
				n.rows = 1
			}
		}
		return nil
	}
	if n.kind == "function" && n.op == "LET" {
		if len(n.args) < 3 || len(n.args)%2 == 0 {
			return errLocalFormula
		}
		scope := maps.Clone(locals)
		if scope == nil {
			scope = map[string]bool{}
		}
		for i := 0; i < len(n.args)-1; i += 2 {
			id, v := n.args[i], n.args[i+1]
			key := strings.TrimPrefix(id.op, "_XLPM.")
			if id.kind != "name" || key == "" || key == "R" || key == "C" || strings.ContainsAny(key, "[]!") {
				return errLocalFormula
			}
			if err := bindLetNames(v, scope); err != nil {
				return err
			}
			id.op, id.kind = key, "localName"
			scope[key] = localFormulaScalar(v)
		}
		return bindLetNames(n.args[len(n.args)-1], scope)
	}
	for _, arg := range n.args {
		if err := bindLetNames(arg, locals); err != nil {
			return err
		}
	}
	return nil
}

func worksheetStoragePrefix(name string) (string, string, bool) {
	if prefix, bare, ok := worksheetArrayName(name); ok {
		return prefix, bare, true
	}
	bare := strings.TrimPrefix(name, "_XLFN.")
	switch bare {
	case "CEILING.MATH", "CEILING.PRECISE", "COMBINA", "CONCAT", "COVARIANCE.P", "COVARIANCE.S", "DAYS", "FLOOR.MATH", "FLOOR.PRECISE", "FORECAST.LINEAR", "IFNA", "IFS", "ISOWEEKNUM", "LET", "MAXIFS", "MINIFS", "MODE.SNGL", "NORM.DIST", "NORM.INV", "NORM.S.DIST", "NORM.S.INV", "NUMBERVALUE", "PDURATION", "PERCENTILE.EXC", "PERCENTILE.INC", "PERMUTATIONA", "QUARTILE.EXC", "QUARTILE.INC", "RANK.AVG", "RANK.EQ", "RRI", "STDEV.P", "STDEV.S", "SWITCH", "TEXTAFTER", "TEXTBEFORE", "TEXTJOIN", "UNICHAR", "UNICODE", "VAR.P", "VAR.S", "XLOOKUP", "XMATCH", "XOR":
		return "_XLFN.", bare, true
	}
	return "", "", false
}
