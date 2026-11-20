package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"excellens/internal/archives"
	"excellens/internal/editfile"
	"excellens/internal/recovery"
	"excellens/internal/workbook"
)

type App struct {
	// Keep the verified base layout and its binding together under mu.
	editLayoutBinding string
	checkedEditLayout editfile.XLSXLayout
	editSourceCells   editfile.SourceCellCache
	calculationMu     sync.Mutex
	calculation       *editCalculationCache

	recoveryOnce     sync.Once
	recoveryStore    *recovery.Store
	recoveryErr      error
	recoverySem      chan struct{}
	archiveMu        sync.Mutex
	archiveView      *archiveView
	archiveTask      *archiveTask
	mu               sync.RWMutex
	cacheMu          *sync.RWMutex // shared by all browser sessions and source pins
	sourceMu         sync.Mutex
	sourcePins       map[*workbook.Workbook]int
	sourceUploads    map[string]int
	compositionBytes int64 // root sourceMu; shared across every browser session
	relationBytes    int64 // root sourceMu; retained inputs and independent plans
	reconcileMu      sync.Mutex
	reconcileBooks   map[string]*reconcileAttachment
	reconcileJobs    map[string]*reconcileTask
	recipeMu         sync.Mutex
	recipeCaptures   map[string]*recipeCaptureTask
	viewMu           sync.Mutex
	bookmarkMu       sync.Mutex
	recentMu         sync.Mutex
	navigationMu     sync.Mutex
	navigationCache  map[string][]occupiedRun
	analysisMu       sync.Mutex
	analysisJobs     map[string]*analysisTask
	analysisTreeJobs map[string]*analysisTreeTask
	analysisSources  map[string]*analysisSourceSnapshot
	analysisSem      chan struct{}
	wb               *workbook.Workbook
	startupError     string
	tempFiles        []string
	indexJobs        map[string]*IndexJob
	indexBySheet     map[string]string
	searchJobs       map[string]*SearchJob
	filterJobs       map[string]*FilterJob
	filterResults    map[string]*FilterResult
	rowViews         map[string]*sheetRowView
	rowViewJobs      map[string]*rowViewTask
	rowViewLatest    map[string]string
	catalogueJobs    map[string]*catalogueTask
	dataRuleJobs     map[string]*dataRuleTask
	printJobs        map[string]*printTask
	clusterJobs      map[string]*clusterTask
	compositionJobs  map[string]*compositionTask
	relationJobs     map[string]*relationTask
	exportJobs       map[string]*ExportJob
	distinctMu       sync.Mutex
	distinctCache    map[string]DistinctCache
	indexSem         chan struct{}
	querySem         chan struct{}
	exportSem        chan struct{}
	inspectSem       chan struct{}
	maintenance      bool
	server           *http.Server
	token            string
	mediaTicket      string
	shutdownOnce     sync.Once
	sessionsMu       sync.Mutex
	sessions         map[string]*browserSession
	owner            *App
}

type IndexJob struct {
	mu         sync.RWMutex
	ID         string
	Sheet      string
	Path       string
	Status     string
	Stage      string
	Progress   float64
	DoneBytes  int64
	TotalBytes int64
	Error      string
	Index      *workbook.SheetIndex
	Started    time.Time
	cancel     context.CancelFunc
	ctx        context.Context
	done       chan struct{}
}

type FilterJob struct {
	mu         sync.RWMutex
	ID         string
	TaskID     string
	Sheet      string
	Status     string
	Stage      string
	Progress   float64
	Scanned    int
	Total      int
	Error      string
	ResultID   string
	Started    time.Time
	cancel     context.CancelFunc
	ctx        context.Context
	done       chan struct{}
	checkpoint *taskCheckpointRun
}

type FilterResult struct {
	ID              string
	Sheet           string
	SheetPath       string
	Rows            *workbook.RowOrder
	Sorted          bool
	PinnedPositions []int // positions of source rows 1..30; immutable and bounded
	Created         time.Time
	Spec            filterSpec // only rules which produced this immutable order
}

type DistinctCache struct {
	Items     []workbook.DistinctValue
	Truncated bool
}

type SearchJob struct {
	mu          sync.RWMutex
	ID          string
	TaskID      string
	Sheet       string
	Query       string
	Mode        string
	Scope       string
	Status      string
	Stage       string
	Progress    float64
	Scanned     int
	Total       int
	SheetsDone  int
	SheetsTotal int
	Matches     []workbook.SearchResult
	Truncated   bool
	Error       string
	Started     time.Time
	cancel      context.CancelFunc
	ctx         context.Context
	done        chan struct{}
	checkpoint  *taskCheckpointRun
}

func New() (*App, error) {
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, fmt.Errorf("生成本机访问令牌失败：%w", err)
	}
	var mediaBytes [32]byte
	if _, err := rand.Read(mediaBytes[:]); err != nil {
		return nil, fmt.Errorf("生成图片访问票据失败：%w", err)
	}
	return &App{
		cacheMu:          &sync.RWMutex{},
		sourcePins:       make(map[*workbook.Workbook]int),
		sourceUploads:    make(map[string]int),
		reconcileBooks:   make(map[string]*reconcileAttachment),
		reconcileJobs:    make(map[string]*reconcileTask),
		recipeCaptures:   make(map[string]*recipeCaptureTask),
		recoverySem:      make(chan struct{}, 2),
		indexJobs:        make(map[string]*IndexJob),
		indexBySheet:     make(map[string]string),
		searchJobs:       make(map[string]*SearchJob),
		filterJobs:       make(map[string]*FilterJob),
		filterResults:    make(map[string]*FilterResult),
		rowViews:         make(map[string]*sheetRowView),
		rowViewJobs:      make(map[string]*rowViewTask),
		rowViewLatest:    make(map[string]string),
		catalogueJobs:    make(map[string]*catalogueTask),
		dataRuleJobs:     make(map[string]*dataRuleTask),
		printJobs:        make(map[string]*printTask),
		clusterJobs:      make(map[string]*clusterTask),
		compositionJobs:  make(map[string]*compositionTask),
		relationJobs:     make(map[string]*relationTask),
		exportJobs:       make(map[string]*ExportJob),
		distinctCache:    make(map[string]DistinctCache),
		indexSem:         make(chan struct{}, 2),
		querySem:         make(chan struct{}, 2),
		exportSem:        make(chan struct{}, 2),
		inspectSem:       make(chan struct{}, 1),
		token:            hex.EncodeToString(tokenBytes[:]),
		mediaTicket:      hex.EncodeToString(mediaBytes[:]),
		navigationCache:  make(map[string][]occupiedRun),
		analysisJobs:     make(map[string]*analysisTask),
		analysisTreeJobs: make(map[string]*analysisTreeTask),
		analysisSources:  make(map[string]*analysisSourceSnapshot),
		analysisSem:      make(chan struct{}, 1),
		sessions:         make(map[string]*browserSession),
	}, nil
}

func (a *App) OpenPath(filePath string) error {
	return a.openPathAs(filePath, "")
}

func (a *App) openPathAs(filePath, displayName string) error {
	if archives.IsPath(filePath) {
		return a.openArchive(context.Background(), filePath, filepath.Base(filePath), 0)
	}
	a.closeArchive()
	wb, release, err := a.openPinnedWorkbook(filePath)
	if err != nil {
		a.mu.Lock()
		a.startupError = err.Error()
		a.mu.Unlock()
		return err
	}
	defer release()
	if displayName != "" {
		wb.SetDisplayName(displayName)
	}
	a.replaceWorkbook(wb, "")
	a.recordRecent(wb)
	return nil
}

func (a *App) replaceWorkbook(wb *workbook.Workbook, newTemp string) {
	a.installWorkbook(wb, newTemp, nil, false)
}

// installWorkbook can atomically reject a stale source-opening intent before
// cancelling jobs or replacing any current view. Ordinary opens use no guard.
func (a *App) installWorkbook(wb *workbook.Workbook, newTemp string, expected *workbook.Workbook, guarded bool) bool {
	a.mu.Lock()
	if guarded && (a.wb != expected || a.maintenance) {
		a.mu.Unlock()
		return false
	}
	oldCatalogues := a.clearCatalogueTasksLocked()
	oldDataRules := a.clearDataRuleTasksLocked()
	oldPrints := clearMetadataTasks(a.printJobs)
	oldClusters := clearMetadataTasks(a.clusterJobs)
	oldCompositions, oldCompositionResources := a.clearCompositionTasksLocked()
	oldRelations, oldRelationResources := a.clearRelationTasksLocked()
	oldIndexes := make([]*IndexJob, 0, len(a.indexJobs))
	oldSearches := make([]*SearchJob, 0, len(a.searchJobs))
	oldFilters := make([]*FilterJob, 0, len(a.filterJobs))
	oldExports := make([]*ExportJob, 0, len(a.exportJobs))
	for _, job := range a.exportJobs {
		job.cancel()
		oldExports = append(oldExports, job)
	}
	for _, job := range a.indexJobs {
		job.cancel()
		oldIndexes = append(oldIndexes, job)
	}
	for _, job := range a.searchJobs {
		job.cancel()
		oldSearches = append(oldSearches, job)
	}
	for _, job := range a.filterJobs {
		job.cancel()
		oldFilters = append(oldFilters, job)
	}
	a.wb = wb
	a.startupError = ""
	a.tempFiles = nil
	if newTemp != "" {
		a.tempFiles = append(a.tempFiles, newTemp)
	}
	a.indexJobs = make(map[string]*IndexJob)
	a.indexBySheet = make(map[string]string)
	a.searchJobs = make(map[string]*SearchJob)
	a.filterJobs = make(map[string]*FilterJob)
	oldResults := a.filterResults
	a.filterResults = make(map[string]*FilterResult)
	oldRowViews := a.clearRowViewsLocked()
	a.exportJobs = make(map[string]*ExportJob)
	a.mu.Unlock()
	waitCatalogueTasks(oldCatalogues)
	waitMetadataTasks(oldDataRules)
	waitMetadataTasks(oldPrints)
	waitMetadataTasks(oldClusters)
	waitCompositionTasks(oldCompositions, oldCompositionResources)
	waitRelationTasks(oldRelations, oldRelationResources)
	a.clearReconciliations()
	a.clearRecipeCaptures()
	for _, result := range oldResults {
		result.close()
	}
	a.clearAnalysisTasks()
	a.navigationMu.Lock()
	a.navigationCache = make(map[string][]occupiedRun)
	a.navigationMu.Unlock()
	a.distinctMu.Lock()
	a.distinctCache = make(map[string]DistinctCache)
	a.distinctMu.Unlock()

	waitJobs(oldIndexes, oldSearches)
	waitFilterJobs(oldFilters)
	waitRowViewTasks(oldRowViews)
	for _, j := range oldExports {
		<-j.done
	}
	return true
}

func waitJobs(indexes []*IndexJob, searches []*SearchJob) {
	for _, job := range indexes {
		<-job.done
	}
	for _, job := range searches {
		<-job.done
	}
}

func waitFilterJobs(filters []*FilterJob) {
	for _, job := range filters {
		<-job.done
	}
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/catalogue/start", a.handleCatalogueStart)
	mux.HandleFunc("/api/catalogue/status", a.handleCatalogueStatus)
	mux.HandleFunc("/api/catalogue/cancel", a.handleCatalogueCancel)
	mux.HandleFunc("/api/catalogue/clear", a.handleCatalogueClear)
	mux.HandleFunc("/api/catalogue/items", a.handleCatalogueItems)
	mux.HandleFunc("/api/catalogue/detail", a.handleCatalogueDetail)
	mux.HandleFunc("/api/catalogue/resolve", a.handleCatalogueResolve)
	mux.HandleFunc("/api/data-rules/start", a.handleDataRuleStart)
	mux.HandleFunc("/api/data-rules/status", a.handleDataRuleStatus)
	mux.HandleFunc("/api/data-rules/cancel", a.handleDataRuleCancel)
	mux.HandleFunc("/api/data-rules/clear", a.handleDataRuleClear)
	mux.HandleFunc("/api/data-rules/items", a.handleDataRuleItems)
	mux.HandleFunc("/api/data-rules/detail", a.handleDataRuleDetail)
	mux.HandleFunc("/api/data-rules/resolve", a.handleDataRuleResolve)
	mux.HandleFunc("/api/printing/start", a.handlePrintStart)
	mux.HandleFunc("/api/printing/status", a.handlePrintStatus)
	mux.HandleFunc("/api/printing/cancel", a.handlePrintCancel)
	mux.HandleFunc("/api/printing/clear", a.handlePrintClear)
	mux.HandleFunc("/api/printing/summary", a.handlePrintSummary)
	mux.HandleFunc("/api/printing/page", a.handlePrintPage)
	mux.HandleFunc("/api/analysis/clusters/start", a.handleClusterStart)
	mux.HandleFunc("/api/analysis/clusters/status", a.handleClusterStatus)
	mux.HandleFunc("/api/analysis/clusters/cancel", a.handleClusterCancel)
	mux.HandleFunc("/api/analysis/clusters/clear", a.handleClusterClear)
	mux.HandleFunc("/api/analysis/clusters/groups", a.handleClusterGroups)
	mux.HandleFunc("/api/analysis/clusters/detail", a.handleClusterDetail)
	mux.HandleFunc("/api/analysis/clusters/confirm", a.handleClusterConfirm)
	mux.HandleFunc("/api/analysis/clusters/resolve", a.handleClusterResolve)
	mux.HandleFunc("/api/analysis/clusters/export", a.handleClusterExport)
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/assets/", a.handleAssets)
	mux.HandleFunc("/api/status", a.handleStatus)
	mux.HandleFunc("/api/reconcile/sources", a.handleReconcileSources)
	mux.HandleFunc("/api/reconcile/fields", a.handleReconcileFields)
	mux.HandleFunc("/api/reconcile/start", a.handleReconcileStart)
	mux.HandleFunc("/api/relation/limits", a.handleRelationLimits)
	mux.HandleFunc("/api/relation/sources", a.handleReconcileSources)
	mux.HandleFunc("/api/relation/start", a.handleRelationStart)
	mux.HandleFunc("/api/relation/status", a.handleRelationStatus)
	mux.HandleFunc("/api/relation/result", a.handleRelationResult)
	mux.HandleFunc("/api/relation/cancel", a.handleRelationCancel)
	mux.HandleFunc("/api/relation/clear", a.handleRelationClear)
	mux.HandleFunc("/api/relation/export", a.handleRelationExport)
	mux.HandleFunc("/api/relation/navigate", a.handleRelationNavigate)
	mux.HandleFunc("/api/composition/sources", a.handleReconcileSources)
	mux.HandleFunc("/api/composition/limits", a.handleCompositionLimits)
	mux.HandleFunc("/api/composition/definitions", a.handleCompositionDefinitions)
	mux.HandleFunc("/api/composition/start", a.handleCompositionStart)
	mux.HandleFunc("/api/composition/status", a.handleCompositionStatus)
	mux.HandleFunc("/api/composition/result", a.handleCompositionResult)
	mux.HandleFunc("/api/composition/cancel", a.handleCompositionCancel)
	mux.HandleFunc("/api/composition/clear", a.handleCompositionClear)
	mux.HandleFunc("/api/reconcile/status", a.handleReconcileStatus)
	mux.HandleFunc("/api/reconcile/cancel", a.handleReconcileCancel)
	mux.HandleFunc("/api/reconcile/clear", a.handleReconcileClear)
	mux.HandleFunc("/api/reconcile/export", a.handleReconcileExport)
	mux.HandleFunc("/api/reconcile/source/remove", a.handleReconcileRemoveSource)
	mux.HandleFunc("/api/reconcile/navigate", a.handleReconcileNavigate)
	mux.HandleFunc("/api/named-views", a.handleNamedViews)
	mux.HandleFunc("/api/analysis/snapshots", a.handleComparisonSnapshots)
	mux.HandleFunc("/api/analysis/snapshots/compare", a.handleSnapshotComparison)
	mux.HandleFunc("/api/named-views/resolve", a.handleNamedViewResolve)
	mux.HandleFunc("/api/recipes", a.handleRecipes)
	mux.HandleFunc("/api/recipes/capture/start", a.handleRecipeCaptureStart)
	mux.HandleFunc("/api/recipes/capture/status", a.handleRecipeCaptureStatus)
	mux.HandleFunc("/api/recipes/capture/cancel", a.handleRecipeCaptureCancel)
	mux.HandleFunc("/api/recipes/capture/commit", a.handleRecipeCaptureCommit)
	mux.HandleFunc("/api/recipes/preview", a.handleRecipePreview)
	mux.HandleFunc("/api/recipes/run/start", a.handleRecipeRunStart)
	mux.HandleFunc("/api/recipes/run/status", a.handleAnalysisQueryStatus)
	mux.HandleFunc("/api/recipes/run/cancel", a.handleAnalysisQueryCancel)
	mux.HandleFunc("/api/recovery", a.handleRecovery)
	mux.HandleFunc("/api/recovery/list", a.handleRecoveryList)
	mux.HandleFunc("/api/analysis/sheet/rows", a.handleAnalysisAppendRows)
	mux.HandleFunc("/api/analysis/issues/materialize", a.handleAnalysisIssuesMaterialize)
	mux.HandleFunc("/api/analysis/issues/export", a.handleAnalysisIssuesExport)
	mux.HandleFunc("/api/recent", a.handleRecent)
	mux.HandleFunc("/api/navigate", a.handleNavigate)
	mux.HandleFunc("/api/open", a.handleOpen)
	mux.HandleFunc("/api/edits", a.handleEditList)
	for _, path := range []string{"start", "state", "base", "grid", "input-kinds", "list-areas", "read", "choices", "pivot-items", "pivot-layout", "copy", "preview", "commit", "save", "open"} {
		mux.HandleFunc("/api/edits/"+path, a.handleEditCommand)
	}
	mux.HandleFunc("/api/edits/download", a.handleEditDownload)
	mux.HandleFunc("/api/archive/list", a.handleArchiveList)
	mux.HandleFunc("/api/archive/open", a.handleArchiveOpen)
	mux.HandleFunc("/api/archive/job", a.handleArchiveJob)
	mux.HandleFunc("/api/sheets", a.handleSheets)
	mux.HandleFunc("/api/styles", a.handleStyles)
	mux.HandleFunc("/api/images", a.handleImages)
	mux.HandleFunc("/api/image/content", a.handleImageContent)
	mux.HandleFunc("/api/index/start", a.handleIndexStart)
	mux.HandleFunc("/api/index/status", a.handleIndexStatus)
	mux.HandleFunc("/api/index/cancel", a.handleIndexCancel)
	mux.HandleFunc("/api/sheet", a.handleSheet)
	mux.HandleFunc("/api/preview", a.handlePreview)
	mux.HandleFunc("/api/cell", a.handleCell)
	mux.HandleFunc("/api/cell/explain", a.handleCellExplanation)
	mux.HandleFunc("/api/record", a.handleRecord)
	mux.HandleFunc("/api/table/header", a.handleTableHeader)
	mux.HandleFunc("/api/range", a.handleRange)
	mux.HandleFunc("/api/autofit", a.handleAutofit)
	mux.HandleFunc("/api/filter/start", a.handleFilterStart)
	mux.HandleFunc("/api/filter/distinct", a.handleFilterDistinct)
	mux.HandleFunc("/api/filter/status", a.handleFilterStatus)
	mux.HandleFunc("/api/filter/cancel", a.handleFilterCancel)
	mux.HandleFunc("/api/filter/clear", a.handleFilterClear)
	mux.HandleFunc("/api/filter/locate", a.handleFilterLocate)
	mux.HandleFunc("/api/row-view/start", a.handleRowViewStart)
	mux.HandleFunc("/api/row-view/status", a.handleRowViewStatus)
	mux.HandleFunc("/api/row-view/cancel", a.handleRowViewCancel)
	mux.HandleFunc("/api/row-view/clear", a.handleRowViewClear)
	mux.HandleFunc("/api/row-view/groups", a.handleRowViewGroups)
	mux.HandleFunc("/api/row-view/locate", a.handleFilterLocate)
	mux.HandleFunc("/api/row-view", a.handleRowView)
	mux.HandleFunc("/api/search/start", a.handleSearchStart)
	mux.HandleFunc("/api/search/validate", a.handleSearchValidate)
	mux.HandleFunc("/api/search/history", a.handleSearchHistory)
	mux.HandleFunc("/api/search/status", a.handleSearchStatus)
	mux.HandleFunc("/api/search/cancel", a.handleSearchCancel)
	mux.HandleFunc("/api/tasks", a.handleTaskCheckpoints)
	mux.HandleFunc("/api/tasks/open", a.handleTaskCheckpointOpen)
	mux.HandleFunc("/api/tasks/resume", a.handleTaskCheckpointResume)
	mux.HandleFunc("/api/tasks/discard", a.handleTaskCheckpointDiscard)
	mux.HandleFunc("/api/view", a.handleView)
	mux.HandleFunc("/api/layout", a.handleLayout)
	mux.HandleFunc("/api/cache", a.handleCache)
	mux.HandleFunc("/api/cache/clear", a.handleCacheClear)
	mux.HandleFunc("/api/exit", a.handleExit)
	mux.HandleFunc("/api/export/start", a.handleExportStart)
	mux.HandleFunc("/api/export/status", a.handleExportStatus)
	mux.HandleFunc("/api/export/cancel", a.handleExportCancel)
	mux.HandleFunc("/api/export/download", a.handleExportDownload)
	mux.HandleFunc("/api/export/folder", a.handleExportFolder)
	mux.HandleFunc("/api/profile", a.handleProfile)
	mux.HandleFunc("/api/selection/stats", a.handleSelectionStats)
	mux.HandleFunc("/api/selection/quick", a.handleSelectionQuick)
	mux.HandleFunc("/api/calculator/evaluate", a.handleCalculatorEvaluate)
	mux.HandleFunc("/api/calculator/formula", a.handleCalculatorFormula)
	mux.HandleFunc("/api/calculator/functions", a.handleCalculatorFunctions)
	mux.HandleFunc("/api/calculator/capture", a.handleCalculatorCapture)
	mux.HandleFunc("/api/calculator/history", a.handleCalculatorHistory)
	mux.HandleFunc("/api/inspect", a.handleInspect)
	mux.HandleFunc("/api/bookmarks", a.handleBookmarks)
	mux.HandleFunc("/api/analysis/workspace", a.handleAnalysisWorkspace)
	mux.HandleFunc("/api/analysis/backup", a.handleAnalysisBackup)
	mux.HandleFunc("/api/analysis/profile", a.handleAnalysisProfile)
	mux.HandleFunc("/api/analysis/sheet/batch", a.handleAnalysisBatch)
	mux.HandleFunc("/api/analysis/fields", a.handleAnalysisFields)
	mux.HandleFunc("/api/analysis/source/capture", a.handleAnalysisSourceCapture)
	mux.HandleFunc("/api/analysis/source/restore", a.handleAnalysisSourceRestore)
	mux.HandleFunc("/api/analysis/source/clear", a.handleAnalysisSourceClear)
	mux.HandleFunc("/api/analysis/sheet", a.handleAnalysisSheet)
	mux.HandleFunc("/api/analysis/sheet/edit", a.handleAnalysisEdit)
	mux.HandleFunc("/api/analysis/sheet/paste", a.handleAnalysisPaste)
	mux.HandleFunc("/api/analysis/sheet/create", a.handleAnalysisCreate)
	mux.HandleFunc("/api/analysis/sheet/columns", a.handleAnalysisColumns)
	mux.HandleFunc("/api/analysis/report", a.handleAnalysisReport)
	mux.HandleFunc("/api/analysis/delete", a.handleAnalysisDelete)
	mux.HandleFunc("/api/analysis/query/start", a.handleAnalysisQueryStart)
	mux.HandleFunc("/api/analysis/query/status", a.handleAnalysisQueryStatus)
	mux.HandleFunc("/api/analysis/query/cancel", a.handleAnalysisQueryCancel)
	mux.HandleFunc("/api/analysis/tree/start", a.handleAnalysisTreeStart)
	mux.HandleFunc("/api/analysis/tree/status", a.handleAnalysisTreeStatus)
	mux.HandleFunc("/api/analysis/tree/cancel", a.handleAnalysisTreeCancel)
	mux.HandleFunc("/api/analysis/tree/clear", a.handleAnalysisTreeClear)
	mux.HandleFunc("/api/analysis/drill", a.handleAnalysisDrill)
	mux.HandleFunc("/api/analysis/composition/navigate", a.handleAnalysisCompositionNavigate)
	mux.HandleFunc("/api/analysis/materialize", a.handleAnalysisMaterialize)
	mux.HandleFunc("/api/analysis/detail/materialize", a.handleAnalysisMaterializeDetail)
	mux.HandleFunc("/api/analysis/dashboard/order", a.handleAnalysisDashboardOrder)
	mux.HandleFunc("/api/analysis/export", a.handleAnalysisExport)
	mux.HandleFunc("/api/settings/analysis-display", a.handleAnalysisDisplay)
	return mux
}

func (a *App) Handler() http.Handler {
	return a.securityHeaders(a.routeSessions(a.routes()))
}

func (a *App) Listen() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("启动本机服务失败：%w", err)
	}
	a.server = &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	return ln, url, nil
}

func (a *App) Serve(ln net.Listener) error {
	if a.server == nil {
		return errors.New("HTTP 服务尚未初始化")
	}
	err := a.server.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) Shutdown() {
	a.shutdownOnce.Do(func() {
		a.mu.Lock()
		for _, job := range a.catalogueJobs {
			job.cancel()
		}
		for _, job := range a.dataRuleJobs {
			job.cancel()
		}
		for _, job := range a.printJobs {
			job.cancel()
		}
		for _, job := range a.clusterJobs {
			job.cancel()
		}
		for _, job := range a.compositionJobs {
			job.cancel()
		}
		for _, job := range a.relationJobs {
			job.cancel()
		}
		for _, job := range a.rowViewJobs {
			job.cancel()
		}
		for _, job := range a.indexJobs {
			job.cancel()
		}
		for _, job := range a.searchJobs {
			job.cancel()
		}
		for _, job := range a.filterJobs {
			job.cancel()
		}
		for _, job := range a.exportJobs {
			job.cancel()
		}
		srv := a.server
		a.mu.Unlock()
		a.analysisMu.Lock()
		for _, job := range a.analysisJobs {
			job.cancel()
		}
		for _, job := range a.analysisTreeJobs {
			job.cancel()
		}
		a.analysisMu.Unlock()
		a.cancelReconciliations()
		a.cancelRecipeCaptures()
		if srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		}
	})
}

func (a *App) Close() {
	a.closeArchive()
	a.Shutdown()
	a.closeBrowserSessions()
	a.clearReconciliations()
	a.clearRecipeCaptures()
	a.clearAnalysisTasks()
	a.mu.Lock()
	catalogues := a.clearCatalogueTasksLocked()
	dataRules := a.clearDataRuleTasksLocked()
	prints := clearMetadataTasks(a.printJobs)
	clusters := clearMetadataTasks(a.clusterJobs)
	compositions, compositionResources := a.clearCompositionTasksLocked()
	relations, relationResources := a.clearRelationTasksLocked()
	a.tempFiles = nil
	indexes := make([]*IndexJob, 0, len(a.indexJobs))
	searches := make([]*SearchJob, 0, len(a.searchJobs))
	filters := make([]*FilterJob, 0, len(a.filterJobs))
	exports := make([]*ExportJob, 0, len(a.exportJobs))
	for _, j := range a.exportJobs {
		exports = append(exports, j)
	}
	for _, job := range a.indexJobs {
		indexes = append(indexes, job)
	}
	for _, job := range a.searchJobs {
		searches = append(searches, job)
	}
	for _, job := range a.filterJobs {
		filters = append(filters, job)
	}
	a.mu.Unlock()
	waitCatalogueTasks(catalogues)
	waitMetadataTasks(dataRules)
	waitMetadataTasks(prints)
	waitMetadataTasks(clusters)
	waitCompositionTasks(compositions, compositionResources)
	waitRelationTasks(relations, relationResources)
	waitJobs(indexes, searches)
	waitFilterJobs(filters)
	for _, j := range exports {
		<-j.done
	}
	a.mu.Lock()
	for id, result := range a.filterResults {
		result.close()
		delete(a.filterResults, id)
	}
	rowViews := a.clearRowViewsLocked()
	a.mu.Unlock()
	waitRowViewTasks(rowViews)
}

func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func ConfigureLog() func() {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return func() {}
	}
	dir := filepath.Join(base, "ExcelLens", "logs")
	if os.MkdirAll(dir, 0700) != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, "viewer.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return func() {}
	}
	log.SetOutput(f)
	return func() { _ = f.Close() }
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !remoteIsLoopback(r.RemoteAddr) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/export/download" && r.URL.Path != "/api/image/content" && r.URL.Path != "/api/edits/download" && r.Header.Get("X-ExcelLens-Token") != a.token {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data: blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func remoteIsLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
