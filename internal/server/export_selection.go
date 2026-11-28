package server

import (
	"context"

	"fmt"

	excelExport "excellens/internal/export"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func (req exportRequest) options() excelExport.Options {
	return excelExport.Options{Format: req.Format, Mode: req.Mode, BOM: req.BOM, ProtectCSV: req.ProtectCSV, Styles: req.Styles}
}

// The ordinary writer and durable export capture share one range assembler.
// The caller owns the cache read lock and any captured selection snapshot.
func (a *App) prepareExportSelections(ctx context.Context, wb *workbook.Workbook, req exportRequest, rows *exportRowSource, stage func(string)) ([]excelExport.Selection, error) {
	selections := make([]excelExport.Selection, 0, 10)
	metas := wb.Sheets
	if req.Scope != "all" {
		meta, found := wb.FindSheet(req.Sheet)
		if !found {
			return nil, i18n.Error("service.exportSelection.exportWorksheetDoesNotExist")
		}
		metas = []workbook.SheetMeta{meta}
	}
	for i, meta := range metas {
		if req.Scope == "all" && !req.IncludeHidden && meta.State != "" && meta.State != "visible" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if stage != nil {
			stage(fmt.Sprintf("建立工作表索引 · %d/%d：%s", i+1, len(metas), meta.Name))
		}
		var index *workbook.SheetIndex
		if rows != nil {
			if rows.path != meta.Path {
				return nil, i18n.Error("service.exportSelection.exportedFlowDoesNotMatch")
			}
			index = rows.index
		} else {
			index = wb.GetCachedIndex(meta.Path)
		}
		if index == nil {
			releaseIndex, admissionErr := a.backgroundIndex(ctx)
			if admissionErr != nil {
				return nil, ctx.Err()
			}
			var err error
			index, err = wb.BuildIndex(meta, nil, ctx.Done())
			releaseIndex()
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, i18n.Wrap("service.exportSelection.index", err, i18n.Args{"name": meta.Name})
			}
		}
		s := excelExport.Selection{Sheet: meta, Index: index, FirstRow: 1, LastRow: max(1, index.MaxRow), FirstCol: 1, LastCol: max(1, index.MaxCol)}
		if req.Scope == "selection" {
			c1, r1, err := workbook.ParseCellRef(req.Start)
			if err != nil {
				return nil, err
			}
			c2, r2, err := workbook.ParseCellRef(req.End)
			if err != nil {
				return nil, err
			}
			s.FirstRow, s.LastRow = min(r1, r2), max(r1, r2)
			s.FirstCol, s.LastCol = min(c1, c2), max(c1, c2)
			s.Compact = true
		} else if req.Scope == "filtered" {
			if rows == nil || rows.snapshot.filter == nil {
				return nil, i18n.Error("service.exportSelection.completeFilterExportLacksMatching")
			}
			s.Filtered, s.Order, s.Compact = true, rows.snapshot.filter.Rows, true
		}
		if rows != nil && rows.plan != nil {
			s.Filtered, s.Compact, s.Rows = true, true, *rows.plan
			if first, last, contiguous := rows.plan.contiguousRange(); contiguous {
				// Preserve the existing merge/layout mapping for physical spans.
				s.FirstRow, s.LastRow, s.Filtered, s.Rows = first, last, false, nil
			}
		}
		selections = append(selections, s)
	}
	if err := excelExport.Validate(req.options(), selections); err != nil {
		return nil, err
	}
	return selections, nil
}
