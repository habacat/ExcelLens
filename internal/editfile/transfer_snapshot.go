package editfile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

// Read the copied revision under the current document lease. Its original
// sparse edits may predate the first structural paste and need their own
// materialization. The clipboard never supplies file paths or raw XML.
func withPriorTransferSource(ctx context.Context, dir string, view *editdoc.Document, write func(*xlsxPackage) (string, error)) (string, error) {
	base, err := workbook.OpenEditSource(view.Base.Source.Path)
	if err != nil {
		return "", err
	}
	defer base.Close()
	if err = VerifySource(ctx, base, view.Base.Source.Stamp); err != nil {
		return "", err
	}
	source := base
	values, templates := view.Snapshot(), view.ColumnFormulas()
	if len(values) > 0 || view.Rules > 0 || len(templates) > 0 {
		var layout Layout
		if err = json.Unmarshal(view.Base.Layout, &layout); err != nil {
			return "", err
		}
		if err = os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
		path := filepath.Join(dir, ".clipboard-overlay")
		defer os.Remove(path)
		if err = materializeOverlay(ctx, path, view, layout, base, values, templates); err != nil {
			return "", err
		}
		source, err = workbook.OpenEditSource(path)
		if err != nil {
			return "", err
		}
		defer source.Close()
	}
	prior, err := openXLSXPackage(source, source.Size())
	if err == nil {
		err = prior.inspect(ctx)
	}
	if err == nil {
		err = prior.inspectGrid(ctx)
	}
	if err != nil {
		return "", err
	}
	result, err := write(prior)
	if err == nil {
		err = base.Check()
	}
	if err != nil && result != "" {
		_ = os.Remove(result)
		return "", err
	}
	return result, err
}
