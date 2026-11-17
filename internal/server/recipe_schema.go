package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/workbook"
)

// A lazy root reads fewer grouping fields, but replay still promises to check
// every declared binding. Keep that guard without building descendant groups.
func recipeScanInputs(scan analysis.Scan, masks map[int]recipeTypeMask) analysis.Scan {
	if len(masks) == 0 {
		return scan
	}
	return func(ctx context.Context, needed []string, visit func(analysis.Record) error) error {
		fields := append([]string{}, needed...)
		seen := make(map[string]bool, len(fields)+len(masks))
		for _, id := range fields {
			seen[id] = true
		}
		for column := range masks {
			id := "C" + strconv.Itoa(column)
			if !seen[id] {
				fields = append(fields, id)
			}
		}
		return scan(ctx, fields, visit)
	}
}

// Profiles describe raw read types AND the current numeric interpretation.
// CSV amount text and native Excel text cannot become interchangeable merely
// because their displayed strings happen to be equal. No date is inferred.
type recipeTypeMask uint16

// A replay plan is built by the server from a validated recipe and preview.
// It is never accepted as an arbitrary client-provided query extension.
type recipeRunPlan struct {
	Stamp        string
	Masks        map[int]recipeTypeMask
	RecipeID     string
	ExportFormat string
}

const (
	recipeNumber recipeTypeMask = 1 << iota
	recipeText
	recipeAmountText
	recipeBoolean
	recipeDate
	recipeError
	recipeMissingCache
	recipeInvalidNumber
	recipeUnknown
	recipeTypeMissing
)

func recipeCellMask(format string, cell workbook.CellData, numeric bool) recipeTypeMask {
	if cell.CacheMissing {
		return recipeMissingCache
	}
	if cell.Raw == "" {
		return 0 // missing/blank inputs do not manufacture a type
	}
	var kind recipeTypeMask
	switch cell.Type {
	case "", "n":
		kind = recipeNumber
		if !numeric {
			kind = recipeInvalidNumber
		}
	case "s", "str", "inlineStr":
		kind = recipeText
		if numeric {
			kind = recipeAmountText
		}
	case "b":
		kind = recipeBoolean
	case "d":
		kind = recipeDate
	case "e":
		kind = recipeError
	default:
		kind = recipeUnknown
	}
	if format == "sqlite" {
		kind |= recipeTypeMissing
	}
	return kind
}

func (mask recipeTypeMask) label() string {
	var labels []string
	for _, pair := range []struct {
		bit  recipeTypeMask
		name string
	}{{recipeNumber, "数值"}, {recipeText, "文本"}, {recipeAmountText, "参与数值统计的金额文本"}, {recipeBoolean, "布尔"}, {recipeDate, "源日期类型"}, {recipeError, "错误值"}, {recipeMissingCache, "公式缺缓存"}, {recipeInvalidNumber, "无效数值"}, {recipeUnknown, "未知类型"}, {recipeTypeMissing, "源类型未保留"}} {
		if mask&pair.bit != 0 {
			labels = append(labels, pair.name)
		}
	}
	if len(labels) == 0 {
		return "未见非空值"
	}
	return strings.Join(labels, " / ")
}

func (mask recipeTypeMask) uncertain() bool {
	return mask&(recipeError|recipeMissingCache|recipeInvalidNumber|recipeUnknown|recipeTypeMissing) != 0
}

func guardRecipeTypes(format string, expected map[int]recipeTypeMask) sourceCellObserver {
	if len(expected) == 0 {
		return nil // a field-free record count must not bind its reader's dummy column
	}
	return func(row int, cell workbook.CellData, numeric bool) error {
		allowed, ok := expected[cell.Col]
		if !ok {
			return fmt.Errorf("分析方案读取了未绑定字段%s，已停止", workbook.ColumnName(cell.Col))
		}
		actual := recipeCellMask(format, cell, numeric)
		if actual&^allowed != 0 {
			return fmt.Errorf("%s%d的读取类型为%s，分析方案预期%s；已停止，未发布部分结果", workbook.ColumnName(cell.Col), row, actual.label(), allowed.label())
		}
		return nil
	}
}
