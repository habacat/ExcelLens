package editfile

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type XLSXSheet struct {
	Name         string `json:"name"`
	Part         string `json:"part"`
	FirstRow     int    `json:"firstRow"`
	FirstCol     int    `json:"firstCol"`
	Rows         int    `json:"rows"`
	Cols         int    `json:"cols"`
	Formulas     int    `json:"formulas,omitempty"`
	Cached       int    `json:"cached,omitempty"`
	SharedCached int    `json:"sharedCached,omitempty"`
	Rules        int    `json:"rules,omitempty"`
	ReadOnly     string `json:"readOnly,omitempty"`
}

// GeneratedArea describes cached results owned by an array anchor or a pivot.
type GeneratedArea struct {
	editdoc.Area
	Input string `json:"input"`
	Pivot bool   `json:"pivot,omitempty"`
}

type XLSXLayout struct {
	checkedSource      string
	checkedLayout      [sha256.Size]byte
	ChartSheets        []string            `json:"chartSheets,omitempty"`
	Pivots             []PivotInfo         `json:"pivots,omitempty"`
	PivotMeasuresReady bool                `json:"pivotMeasuresReady,omitempty"`
	PivotDatesReady    bool                `json:"pivotDatesReady,omitempty"`
	PivotAxesReady     bool                `json:"pivotAxesReady,omitempty"`
	Tables             []xlsxTable         `json:"tables,omitempty"`
	Inputs             map[string][]string `json:"inputs,omitempty"`
	Generated          []GeneratedArea     `json:"generated,omitempty"`
	Size               int64               `json:"size"`
	Sheets             []XLSXSheet         `json:"sheets"`
	Rules              []editdoc.Rule      `json:"rules,omitempty"`
	GridEditing        bool                `json:"gridEditing,omitempty"`
	Structured         bool                `json:"structured,omitempty"`
	Native             bool                `json:"native,omitempty"`
	MacroEnabled       bool                `json:"macroEnabled,omitempty"`
	RecalcOnOpen       bool                `json:"recalcOnOpen,omitempty"`
}

func (l XLSXLayout) Valid() bool {
	if !l.validTables() || l.Size <= 0 || l.Size > formats.EditOutputBytes || len(l.Sheets) == 0 || len(l.Sheets) > formats.EditOfficeParts || len(l.Rules) > formats.EditValidationRules || l.Native && !l.Structured {
		return false
	}
	names, parts := map[string]bool{}, map[string]bool{}
	for _, s := range l.Sheets {
		name := strings.ToLower(s.Name)
		if name == "" || len(name) > 512 || names[name] || parts[s.Part] || !sheetPart.MatchString(s.Part) || s.Rows < 0 || s.Rows > formats.ExcelRows || s.Cols < 0 || s.Cols > formats.ExcelColumns || s.FirstRow < 0 || s.FirstRow > s.Rows || s.FirstCol < 0 || s.FirstCol > s.Cols ||
			s.Formulas < 0 || s.Formulas > formats.EditXLSXPartBytes || s.Cached < 0 || s.Cached > s.Formulas || s.SharedCached < 0 || s.SharedCached > s.Cached || s.Rules < 0 || s.Rules > formats.EditValidationRules {
			return false
		}
		if s.ReadOnly != "" && (!l.Structured || !wholeSheetLock(s.ReadOnly)) {
			return false
		}
		names[name], parts[s.Part] = true, true
	}
	for sheet, inputs := range l.Inputs {
		if !names[strings.ToLower(sheet)] {
			return false
		}
		for _, input := range inputs {
			if !names[strings.ToLower(input)] {
				return false
			}
		}
	}
	for _, a := range l.Generated {
		if !a.Area.Valid() || !names[strings.ToLower(a.Start.Sheet)] || !names[strings.ToLower(a.Input)] {
			return false
		}
	}
	counts := map[string]int{}
	for i, rule := range l.Rules {
		if !rule.Valid() {
			return false
		}
		for _, prev := range l.Rules[:i] {
			if prev.Intersects(rule.Area) {
				return false
			}
		}
		counts[rule.Start.Sheet]++
	}
	for _, s := range l.Sheets {
		if counts[s.Name] != s.Rules {
			return false
		}
		delete(counts, s.Name)
	}
	if len(counts) != 0 {
		return false
	}
	return true
}

func (l XLSXLayout) HasSheet(name string) bool {
	for _, sheet := range l.Sheets {
		if strings.EqualFold(sheet.Name, name) {
			return true
		}
	}
	return false
}

// The seal lives only in memory. A restored layout or edited metadata takes
// the full preflight path. The generated workbook always has its own checks.
func (l *XLSXLayout) signature() [sha256.Size]byte {
	data, err := json.Marshal(l)
	if err != nil {
		return [sha256.Size]byte{}
	}
	return sha256.Sum256(data)
}

func InspectXLSX(ctx context.Context, f *workbook.EditSourceFile, wb *workbook.Workbook) (XLSXLayout, string, error) {
	proof, err := f.SHA256(ctx)
	if err != nil {
		return XLSXLayout{}, "", err
	}
	p, err := openXLSXPackage(f, f.Size())
	if err == nil {
		err = p.inspect(ctx)
	}
	if err != nil {
		return XLSXLayout{}, "", err
	}
	if p.layout.MacroEnabled != strings.EqualFold(filepath.Ext(wb.Path), ".xlsm") {
		return XLSXLayout{}, "", fmt.Errorf("%w：工作簿内容类型与扩展名不匹配", ErrXLSX)
	}
	if len(p.layout.Sheets) != len(wb.Sheets) {
		return XLSXLayout{}, "", ErrBinding
	}
	for i, s := range p.layout.Sheets {
		if s.Name != wb.Sheets[i].Name || s.Part != wb.Sheets[i].Path {
			return XLSXLayout{}, "", ErrBinding
		}
	}
	if err = p.inspectGrid(ctx); err != nil {
		return XLSXLayout{}, "", err
	}
	bound := Layout{XLSX: &p.layout}
	refs, err := bound.referenceRules(p.layout.Rules)
	if err != nil {
		return XLSXLayout{}, "", fmt.Errorf("%w：%w", ErrXLSX, err)
	}
	if len(refs) != 0 {
		targets, err := appendReferences(nil, refs)
		if err != nil {
			return XLSXLayout{}, "", fmt.Errorf("%w：%w", ErrXLSX, err)
		}
		values, err := p.readCells(ctx, targets)
		if err != nil {
			return XLSXLayout{}, "", err
		}
		for _, rule := range refs {
			if _, err := rule.referencedChoices(func(a editdoc.Address) editdoc.Value { return values[a].Value }); err != nil {
				return XLSXLayout{}, "", fmt.Errorf("%w：%w", ErrXLSX, err)
			}
		}
	}
	if err = VerifySource(ctx, f, proof); err != nil {
		return XLSXLayout{}, "", err
	}
	p.layout.checkedSource, p.layout.checkedLayout = proof, p.layout.signature()
	return p.layout, proof, nil
}

func (p *xlsxPackage) inspectGrid(ctx context.Context) error {
	if err := p.inspectGridCoordinates(ctx); err != nil {
		return err
	}
	for _, sheet := range p.layout.Sheets {
		if len(p.chartPartsFor(sheet.Name)) > 0 {
			p.layout.ChartSheets = append(p.layout.ChartSheets, sheet.Name)
		}
	}
	p.layout.PivotAxesReady = len(p.pivotOutputs) > 0
	p.layout.PivotMeasuresReady = p.layout.PivotAxesReady
	p.layout.PivotDatesReady = p.layout.PivotAxesReady
	return p.inspectPivotFields(ctx)
}

func (p *xlsxPackage) inspectGridCoordinates(ctx context.Context) error {
	p.layout.GridEditing = true
	if p.layout.Native {
		p.layout.RecalcOnOpen = p.staleFormulaCaches
		return p.checkNativeTableBindings(ctx)
	}
	err := p.inspectWorksheets(ctx)
	if !p.layout.Structured && errors.Is(err, errStructuredXLSX) {
		p.layout.Structured = true
		err = p.inspectWorksheets(ctx)
	}
	if errors.Is(err, errOfficeWorksheet) {
		p.layout.Native, p.layout.Structured, p.layout.RecalcOnOpen = true, true, p.staleFormulaCaches
		if err = p.inspectOfficeParts(ctx); err != nil {
			return err
		}
		return p.checkNativeTableBindings(ctx)
	}
	return err
}

// UpgradeGridLayout rederives coordinate ownership from the digest-bound
// base while keeping the document identity, transactions and artifacts.
func UpgradeGridLayout(ctx context.Context, f *workbook.EditSourceFile, old XLSXLayout) (XLSXLayout, error) {
	if f.Size() != old.Size {
		return XLSXLayout{}, ErrBinding
	}
	p, err := openXLSXPackage(f, f.Size())
	if err == nil {
		err = p.inspect(ctx)
	}
	if err == nil {
		err = p.inspectGrid(ctx)
	}
	if err != nil {
		return XLSXLayout{}, err
	}
	if len(old.Sheets) != len(p.layout.Sheets) || !slices.Equal(old.Rules, p.layout.Rules) {
		return XLSXLayout{}, ErrBinding
	}
	for i, prior := range old.Sheets {
		actual := p.layout.Sheets[i]
		prior.ReadOnly = actual.ReadOnly
		if prior != actual {
			return XLSXLayout{}, ErrBinding
		}
	}
	return p.layout, f.Check()
}
