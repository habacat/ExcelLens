package server

import "net/http"

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	a.registerWorkbookRoutes(mux)
	a.registerQueryRoutes(mux)
	a.registerAnalysisRoutes(mux)
	a.registerEditRoutes(mux)
	a.registerExportRoutes(mux)
	a.registerComparisonRoutes(mux)
	a.registerTaskRoutes(mux)
	a.registerInspectionRoutes(mux)
	return mux
}

func (a *App) registerWorkbookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/assets/", a.handleAssets)
	mux.HandleFunc("/api/notes", a.handleSourceNotes)
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/named-views", a.handleNamedViews)
	mux.HandleFunc("/api/named-views/resolve", a.handleNamedViewResolve)
	mux.HandleFunc("/api/recent", a.handleRecent)
	mux.HandleFunc("/api/navigate", a.handleNavigate)
	mux.HandleFunc("/api/open", a.handleOpen)
	mux.HandleFunc("/api/archive/list", a.handleArchiveList)
	mux.HandleFunc("/api/archive/open", a.handleArchiveOpen)
	mux.HandleFunc("/api/archive/job", a.handleArchiveJob)
	mux.HandleFunc("/api/sheets", a.handleSheets)
	mux.HandleFunc("/api/styles", a.handleStyles)
	mux.HandleFunc("/api/images", a.handleImages)
	mux.HandleFunc("/api/image/content", a.handleImageContent)
	mux.HandleFunc("/api/sheet", a.handleSheet)
	mux.HandleFunc("/api/preview", a.handlePreview)
	mux.HandleFunc("/api/cell", a.handleCell)
	mux.HandleFunc("/api/cell/explain", a.handleCellExplanation)
	mux.HandleFunc("/api/record", a.handleRecord)
	mux.HandleFunc("/api/table/header", a.handleTableHeader)
	mux.HandleFunc("/api/range", a.handleRange)
	mux.HandleFunc("/api/autofit", a.handleAutofit)
	mux.HandleFunc("/api/view", a.handleView)
	mux.HandleFunc("/api/layout", a.handleLayout)
	mux.HandleFunc("/api/cache", a.handleCache)
	mux.HandleFunc("/api/cache/clear", a.handleCacheClear)
	mux.HandleFunc("/api/exit", a.handleExit)
	mux.HandleFunc("/api/profile", a.handleProfile)
	mux.HandleFunc("/api/selection/stats", a.handleSelectionStats)
	mux.HandleFunc("/api/selection/quick", a.handleSelectionQuick)
	mux.HandleFunc("/api/inspect", a.handleInspect)
	mux.HandleFunc("/api/bookmarks", a.handleBookmarks)
}
