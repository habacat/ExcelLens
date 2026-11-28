package server

import (
	"context"

	"slices"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

// Caller owns the source pin and cache read lock for the complete operation.
// Reuse the public adapter/projection, with a separate explicit relation limit.
func (a *App) buildRelationSource(ctx context.Context, book *workbook.Workbook, ref reconcileSourceRef, columns []int) (reconcile.Source, error) {
	columns = slices.Clone(columns)
	slices.Sort(columns)
	columns = slices.Compact(columns)
	source, err := a.buildTypedTableSource(ctx, book, ref, columns, formats.RelationSourceRowBytes, formats.RelationColumns+reconcile.MaxKeys)
	if err != nil {
		return reconcile.Source{}, err
	}
	stamp := source.Info.Stamp
	source.Check = func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := book.CheckSourceUnchanged(); err != nil {
			return err
		}
		if strings.HasPrefix(ref.Source, "scratch:") {
			workspace, err := a.loadAnalysis(book)
			if err != nil {
				return err
			}
			sheet := workspace.Sheet(strings.TrimPrefix(ref.Source, "scratch:"))
			if sheet == nil || analysisSheetStamp(sheet) != stamp {
				return i18n.Error("service.relationSource.associatedSourceAnalysisTableModified")
			}
		}
		return ctx.Err()
	}
	return source, nil
}
