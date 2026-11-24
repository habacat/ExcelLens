package server

import (
	"net/url"
	"strconv"
	"strings"

	"excellens/internal/workbook"
)

// Caller has checked source identity/coordinates and holds a source lease.
// The cache lock must be released before a new browser session is installed.
func (a *App) sourceNavigation(book *workbook.Workbook, kind, source, sheet string, row, col int) (map[string]any, error) {
	if a.currentWorkbook() == book {
		return map[string]any{"sameBook": true, "kind": kind, "sheet": sheet, "analysisSheet": strings.TrimPrefix(source, "scratch:"), "row": row, "col": col}, nil
	}
	params := url.Values{}
	if kind == "analysis" {
		params.Set("analysisSheet", strings.TrimPrefix(source, "scratch:"))
		params.Set("sourceRow", strconv.Itoa(row))
		params.Set("sourceCol", strconv.Itoa(col))
	} else {
		params.Set("sourceSheet", sheet)
		params.Set("sourceRef", workbook.ColumnName(col)+strconv.Itoa(row))
	}
	id := randomID()
	root := a.rootApp()
	session, err := root.acquireSession(id, "", sessionAccess{})
	if err != nil {
		return nil, err
	}
	defer root.releaseSession(session)
	session.app.replaceWorkbook(book)
	params.Set("session", id)
	return map[string]any{"sameBook": false, "url": "/?" + params.Encode()}, nil
}
