package workbook

import (
	"context"

	"excellens/internal/searchresults"
)

func (w *Workbook) NewSearchResults(ctx context.Context) (*searchresults.Store, error) {
	dir := filterOrderDirectory(w)
	if err := processQueryQuota.cleanStale(ctx, dir); err != nil {
		return nil, err
	}
	f, err := newQueryFile(dir, &processQueryQuota)
	if err != nil {
		return nil, err
	}
	return searchresults.New(f, f.file, f.discard), nil
}
