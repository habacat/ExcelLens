package workbook

import (
	"strings"

	"excellens/internal/conditional"
	"excellens/internal/formats"
)

// A duplicate or unavailable local declaration still shadows the global name.
func registerWorkbookName(global map[string]string, local map[string]map[string]string, sheet, name, value string) {
	target := global
	if sheet != "" {
		scope := strings.ToLower(sheet)
		if local[scope] == nil {
			local[scope] = make(map[string]string)
		}
		target = local[scope]
	}
	key := strings.ToLower(name)
	if _, exists := target[key]; exists {
		value = ""
	}
	target[key] = value
}

func (w *Workbook) conditionalDefinedName(sheetName, name string) (conditionalReference, string) {
	var def string
	var local, known bool
	if w != nil {
		key := strings.ToLower(name)
		def, local = w.LocalNamedRanges[strings.ToLower(sheetName)][key]
		known = local
		if !known {
			def, known = w.NamedRanges[key]
		}
	}
	if !known || def == "" {
		return conditionalReference{}, "条件格式定义名称无法解析：" + conditionalNotice(name)
	}
	sheet, address, qualified, err := SplitSheetReference(strings.TrimPrefix(strings.TrimSpace(def), "="))
	ref, valid := conditional.ParseCellRef(address)
	if err != nil || !valid || !ref.AbsoluteRow || !ref.AbsoluteCol || (!qualified && !local) {
		return conditionalReference{}, "条件格式定义名称未指向固定单元格：" + conditionalNotice(name)
	}
	if qualified && !strings.EqualFold(sheet, sheetName) {
		target, ok := w.FindSheet(sheet)
		if !ok {
			return conditionalReference{}, "条件格式定义名称引用了不可用的工作表：" + conditionalNotice(name)
		}
		ref.ForeignSheet = target.Name
	}
	return ref, ""
}

type conditionalForeignAddress struct {
	sheet    string
	row, col int
}

const foreignBudgetNotice = "跨表条件格式引用超过本次读取容量，可先打开引用的工作表建立索引"

func (r *conditionalRead) resolveForeignCell(sheet string, row, col int) (CellData, string, error) {
	key := conditionalForeignAddress{sheet, row, col}
	if cell, ok := r.foreignValues[key]; ok {
		return cell, "", nil
	}
	if r.dependencyReads <= 0 || r.foreignOpens <= 0 || r.dependencyBytes <= 0 {
		return CellData{}, foreignBudgetNotice, nil
	}
	r.dependencyReads--
	r.foreignOpens--
	meta, ok := r.w.FindSheet(sheet)
	if !ok {
		return CellData{}, "定义名称的目标工作表不可用", nil
	}
	idx, err := r.w.CachedIndexForRead(r.ctx, meta.Path)
	var cell CellData
	if err == nil && idx != nil {
		pos := idx.FindRowOffset(row)
		if pos < idx.RowCount() && idx.RowAt(pos).Number == row {
			off := idx.RowAt(pos)
			size := off.End - off.Start
			if size <= 0 || size > int64(r.dependencyBytes) {
				return CellData{}, foreignBudgetNotice, nil
			}
			r.dependencyBytes -= int(size)
			var rows []RowData
			rows, err = r.w.ReadRowsWithOptionsContext(r.ctx, idx, pos, 1, col, col, RowReadOptions{})
			if err == nil && len(rows) == 1 && len(rows[0].Cells) > 0 {
				cell = rows[0].Cells[0]
			}
		}
	} else if err == nil {
		cell, err = r.readForeignPrefix(meta, row, col)
	}
	if err != nil {
		if r.ctx.Err() != nil {
			return CellData{}, "", r.ctx.Err()
		}
		if r.dependencyBytes <= 0 {
			return CellData{}, foreignBudgetNotice, nil
		}
		return CellData{}, "定义名称引用读取失败：" + conditionalNotice(err.Error()), nil
	}
	cell = conditionalOperand(cell)
	cost := 512 + 2*(len(cell.Raw)+len(cell.Type)+len(sheet))
	if cost <= formats.ConditionalDependencyCacheBytes-r.cacheBytes && len(r.foreignValues) < formats.ConditionalDependencyReads {
		if r.foreignValues == nil {
			r.foreignValues = make(map[conditionalForeignAddress]CellData)
		}
		r.foreignValues[key] = cell
		r.cacheBytes += cost
	}
	return cell, "", nil
}
