package editfile

import (
	"context"
	"encoding/xml"
	"excellens/internal/workbook"
	"slices"
	"strings"
)

type FormulaSymbol struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Scope   string   `json:"scope,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Ref     string   `json:"ref,omitempty"`
}

// The catalog is fetched only on formula entry. Older saved layouts do not
// need a new identity: names come from the currently bound workbook part.
func (l XLSXLayout) FormulaSymbols(ctx context.Context, file *workbook.EditSourceFile) ([]FormulaSymbol, error) {
	p, err := openXLSXPackage(file, file.Size())
	if err != nil {
		return nil, err
	}
	p.layout = l
	raw, err := p.readPart(ctx, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	w := workbookExtensions{p: p, seen: map[string]bool{}}
	err = walkXML(ctx, raw, func(e xmlEvent) error {
		if len(e.stack) > 1 && e.stack[1] == (xml.Name{Space: mainNS, Local: "definedNames"}) {
			return w.readDefinedName(e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err = w.resolveNames(ctx); err != nil {
		return nil, err
	}
	var items []FormulaSymbol
	for _, n := range w.names {
		if n.Hidden || strings.HasPrefix(strings.ToLower(n.Name), "_xlnm.") {
			continue
		}
		if _, ok := p.names[nameKey{n.Scope, strings.ToUpper(n.Name)}]; ok {
			items = append(items, FormulaSymbol{Name: n.Name, Scope: n.Scope, Kind: "name"})
		}
	}
	for _, t := range l.Tables {
		v := FormulaSymbol{Name: t.Name, Kind: "table", Scope: t.Sheet, Ref: t.Ref}
		for _, col := range t.Columns {
			v.Columns = append(v.Columns, col.Name)
		}
		items = append(items, v)
	}
	slices.SortStableFunc(items, func(a, b FormulaSymbol) int { return strings.Compare(a.Name, b.Name) })
	return items, file.Check()
}
