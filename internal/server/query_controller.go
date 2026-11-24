package server

import (
	"context"
	"sync"
	"time"

	"excellens/internal/searchresults"
	"excellens/internal/workbook"
)

// Each workbook binding owns its queries, filtered orders and row-view leases.
// Registration and detachment use the same session mutex as the workbook switch.
type queryController struct {
	navigationCache map[string][]occupiedRun
	distinctCache   map[string]DistinctCache
	indexJobs       map[string]*IndexJob
	indexBySheet    map[string]string
	searchJobs      map[string]*SearchJob
	filterJobs      map[string]*FilterJob
	filterResults   map[string]*FilterResult
	rowViews        map[string]*sheetRowView
	rowViewJobs     map[string]*rowViewTask
	rowViewLatest   map[string]string
}

func newQueryController() queryController {
	return queryController{
		navigationCache: make(map[string][]occupiedRun),
		distinctCache:   make(map[string]DistinctCache),
		indexJobs:       make(map[string]*IndexJob),
		indexBySheet:    make(map[string]string),
		searchJobs:      make(map[string]*SearchJob),
		filterJobs:      make(map[string]*FilterJob),
		filterResults:   make(map[string]*FilterResult),
		rowViews:        make(map[string]*sheetRowView),
		rowViewJobs:     make(map[string]*rowViewTask),
		rowViewLatest:   make(map[string]string),
	}
}

func (q *queryController) cancelIndexSearchLocked() []<-chan struct{} {
	done := make([]<-chan struct{}, 0, len(q.indexJobs)+len(q.searchJobs))
	for _, job := range q.indexJobs {
		job.cancel()
		done = append(done, job.done)
	}
	for _, job := range q.searchJobs {
		job.cancel()
		done = append(done, job.done)
	}
	return done
}

func (q *queryController) cancelLocked() {
	for _, job := range q.indexJobs {
		job.cancel()
	}
	for _, job := range q.searchJobs {
		job.cancel()
	}
	for _, job := range q.filterJobs {
		job.cancel()
	}
	for _, job := range q.rowViewJobs {
		job.cancel()
	}
}

// Workers publish through App under the session lock. Once detached, these
// registries belong to the caller and can be drained without holding that lock.
func (q *queryController) detachLocked() queryController {
	q.cancelLocked()
	old := *q
	*q = newQueryController()
	return old
}

func (q *queryController) waitAndRelease() {
	for _, job := range q.indexJobs {
		<-job.done
	}
	for _, job := range q.searchJobs {
		<-job.done
		job.Results.Close()
	}
	for _, job := range q.filterJobs {
		<-job.done
	}
	for _, job := range q.rowViewJobs {
		<-job.done
	}
	for _, view := range q.rowViews {
		view.close()
	}
	for _, result := range q.filterResults {
		result.close()
	}
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
	Full        bool
	Ticket      string
	Results     *searchresults.Store
	HitCount    int64
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
