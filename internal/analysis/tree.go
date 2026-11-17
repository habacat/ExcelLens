package analysis

import (
	"context"
	"errors"
	"unicode/utf8"
)

// Path identifies the parent by exact transformed values, including genuine
// empty strings. It never contains temporary row IDs or synthetic filters.
type TreeLevel struct {
	Path        []string `json:"path"`
	Levels      int      `json:"levels"`
	HasChildren bool     `json:"hasChildren"`
}

func validateLayout(q Query) error {
	if q.Layout == "" {
		return nil
	}
	if q.Layout != "tree" || len(q.Rows) < 1 || len(q.Rows) > 3 {
		return errors.New("透视布局无效；层级透视需要1至3个行维度")
	}
	seen := make(map[string]bool, len(q.Rows))
	for _, id := range q.Rows {
		if id == "" || seen[id] {
			return errors.New("层级透视的行维度不能为空或重复")
		}
		seen[id] = true
	}
	return nil
}

func ValidateTreePath(q Query, path []string) error {
	if err := validateLayout(q); err != nil {
		return err
	}
	if q.Layout != "tree" || len(path) >= len(q.Rows) {
		return errors.New("展开路径不是有效的父级，已到末级时请查看明细")
	}
	for _, key := range path {
		if !utf8.ValidString(key) {
			return errors.New("展开路径包含无效文字")
		}
	}
	return nil
}

func matchTreePath(values map[string]Value, fields, path []string) bool {
	for i, key := range path {
		if values[fields[i]].Text != key {
			return false
		}
	}
	return true
}

// RunTreeLevel streams the same transforms, filters, diagnostics and exact
// accumulators as Run. Only the next level is grouped; no descendant groups,
// row vectors, extra filters or recursively summed aggregates are created.
func RunTreeLevel(ctx context.Context, q Query, fields []Field, path []string, scan Scan, progress func(int64)) (Result, error) {
	if err := ValidateTreePath(q, path); err != nil {
		return Result{}, err
	}
	result, err := runLevel(ctx, q, fields, q.Rows[:len(path)+1], path, scan, progress)
	if err != nil {
		return Result{}, err
	}
	result.Tree = &TreeLevel{Path: append([]string{}, path...), Levels: len(q.Rows), HasChildren: len(path)+1 < len(q.Rows)}
	return result, nil
}

func validateDetailKeys(q Query, keys []string) error {
	if len(keys) == len(q.Rows) || q.Layout == "tree" && len(keys) <= len(q.Rows) {
		return nil
	}
	return errors.New("明细分组条件与透视维度不匹配")
}
