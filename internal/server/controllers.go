package server

import (
	"sync"

	"excellens/internal/formats"
	"excellens/internal/taskmanager"
	"excellens/internal/workbook"
)

// Sub-controllers embedded in App group each subsystem's state together with
// the mutex that protects it, keeping lock ownership and lifecycle teardown
// localized instead of scattering dozens of unrelated fields on App.

type archiveController struct {
	archiveMu   sync.Mutex
	archiveView *archiveView
	archiveTask *archiveTask
}

// sourcePinController lives on the root App; child sessions share pins and
// cross-session byte budgets through a.rootApp().
type sourcePinController struct {
	sourcePins        *taskmanager.PinSet[*workbook.Workbook]
	sourceUploads     *taskmanager.PinSet[string]
	compositionBudget *taskmanager.ByteBudget
	relationBudget    *taskmanager.ByteBudget
}

func newSourcePinController() sourcePinController {
	return sourcePinController{
		sourcePins:        taskmanager.NewPinSet[*workbook.Workbook](),
		sourceUploads:     taskmanager.NewPinSet[string](),
		compositionBudget: taskmanager.NewByteBudget(formats.CompositionRetainedBytes),
		relationBudget:    taskmanager.NewByteBudget(formats.RelationRetainedBytes),
	}
}

type reconcileController struct {
	reconcileMu    sync.Mutex
	reconcileBooks map[string]*reconcileAttachment
	reconcileJobs  map[string]*reconcileTask
}

func newReconcileController() reconcileController {
	return reconcileController{
		reconcileBooks: make(map[string]*reconcileAttachment),
		reconcileJobs:  make(map[string]*reconcileTask),
	}
}

type recipeController struct {
	recipeMu       sync.Mutex
	recipeCaptures map[string]*recipeCaptureTask
}

func newRecipeController() recipeController {
	return recipeController{
		recipeCaptures: make(map[string]*recipeCaptureTask),
	}
}

type analysisController struct {
	analysisMu       sync.Mutex
	analysisJobs     map[string]*analysisTask
	analysisTreeJobs map[string]*analysisTreeTask
	analysisSources  map[string]*analysisSourceSnapshot
}

func newAnalysisController() analysisController {
	return analysisController{
		analysisJobs:     make(map[string]*analysisTask),
		analysisTreeJobs: make(map[string]*analysisTreeTask),
		analysisSources:  make(map[string]*analysisSourceSnapshot),
	}
}

func (c *analysisController) cancelAll() {
	c.analysisMu.Lock()
	defer c.analysisMu.Unlock()
	for _, job := range c.analysisJobs {
		job.cancel()
	}
	for _, job := range c.analysisTreeJobs {
		job.cancel()
	}
}

// metadataJobs groups the per-workbook asynchronous metadata and composition
// registries protected by workbookSession.mu.
type metadataJobs struct {
	catalogueJobs   map[string]*catalogueTask
	dataRuleJobs    map[string]*dataRuleTask
	printJobs       map[string]*printTask
	clusterJobs     map[string]*clusterTask
	compositionJobs map[string]*compositionTask
	relationJobs    map[string]*relationTask
}

func newMetadataJobs() metadataJobs {
	return metadataJobs{
		catalogueJobs:   make(map[string]*catalogueTask),
		dataRuleJobs:    make(map[string]*dataRuleTask),
		printJobs:       make(map[string]*printTask),
		clusterJobs:     make(map[string]*clusterTask),
		compositionJobs: make(map[string]*compositionTask),
		relationJobs:    make(map[string]*relationTask),
	}
}

func (m *metadataJobs) cancelAllLocked() {
	for _, job := range m.catalogueJobs {
		job.cancel()
	}
	for _, job := range m.dataRuleJobs {
		job.cancel()
	}
	for _, job := range m.printJobs {
		job.cancel()
	}
	for _, job := range m.clusterJobs {
		job.cancel()
	}
	for _, job := range m.compositionJobs {
		job.cancel()
	}
	for _, job := range m.relationJobs {
		job.cancel()
	}
}
