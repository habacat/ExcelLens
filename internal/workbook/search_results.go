package workbook

import (
	"context"

	"excellens/internal/rowindex"
	"excellens/internal/searchresults"
)

func (w *Workbook) NewSearchResults(ctx context.Context) (*searchresults.Store, error) {
	f, err := rowindex.OpenTempFile(ctx, filterOrderDirectory(w))
	if err != nil {
		return nil, err
	}
	return searchresults.New(f, f.File(), f.Discard), nil
}
