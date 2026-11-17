package server

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"excellens/internal/analysis"
)

func compositionSaveRequest(a *App, jobID, id string, revision int64) map[string]any {
	wb := a.currentWorkbook()
	return map[string]any{"action": "save", "jobId": jobID, "id": id, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "expectedRevision": revision}
}

func compositionStore(t *testing.T, a *App) compositionDefinitionStore {
	t.Helper()
	return decodeResponse[compositionDefinitionStore](t, analysisRequest(t, a, "GET", "/api/composition/definitions", ""))
}

func TestCompositionDefinitionsPersistOnlyRulesAndRetryAfterTaskEvictionAndRestart(t *testing.T) {
	a, file := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	definition.Fields = append(definition.Fields, "公式原状态")
	definition.Inputs[0].Columns = append(definition.Inputs[0].Columns, 6)
	definition.Inputs[1].Columns = append(definition.Inputs[1].Columns, 0)
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, job, "done")
	request := compositionSaveRequest(a, job.ID, "", 0)
	first := decodeResponse[struct {
		ID       string
		Revision int64
	}](t, reconcilePost(t, a, "/api/composition/definitions", request))
	store := compositionStore(t, a)
	if first.ID != job.ID || first.Revision != 1 || store.Revision != 1 || len(store.Items) != 1 || store.Items[0].CaptureID != job.ID || !reflect.DeepEqual(store.Items[0].Definition, definition) {
		t.Fatal("saved definition lost mapping/source identity or copied a different result", first, store)
	}
	path, _ := compositionDefinitionsPath()
	contents, err := os.ReadFile(path)
	if err != nil || bytes.Contains(contents, []byte("9007199254740993.31")) || bytes.Contains(contents, []byte("SUM(B2:B2)")) {
		t.Fatal("definition catalog included source cell data/formula rather than only rules", err)
	}
	clearComposition(t, a, job.ID)
	b, _ := recordOpen(t, file)
	request = compositionSaveRequest(b, job.ID, "", 0)
	again := decodeResponse[struct {
		ID       string
		Revision int64
	}](t, reconcilePost(t, b, "/api/composition/definitions", request))
	if again != first || compositionStore(t, b).Revision != 1 {
		t.Fatal("lost save confirmation required a live task or duplicated catalog entry", again)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, contents) {
		t.Fatal("idempotent confirmation rewrote the saved catalog")
	}
}

func TestCompositionDefinitionOverwriteNameConflictAndStaleDeleteAreAtomic(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	one := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, one, "done")
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/composition/definitions", compositionSaveRequest(a, one.ID, "", 0)))
	definition.Name = "第二个联合来源"
	two := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition, KeepID: one.ID})
	waitComposition(t, a, two, "done")
	if response := reconcilePost(t, a, "/api/composition/definitions", compositionSaveRequest(a, two.ID, "", 0)); response.Code != 409 {
		t.Fatal("stale new save ignored catalog revision", response.Code)
	}
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/composition/definitions", compositionSaveRequest(a, two.ID, "", 1)))
	definition.Name = "更新后的第一来源"
	three := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition, KeepID: two.ID})
	waitComposition(t, a, three, "done")
	if response := reconcilePost(t, a, "/api/composition/definitions", compositionSaveRequest(a, three.ID, one.ID, 1)); response.Code != 409 {
		t.Fatal("stale overwrite replaced a definition", response.Code)
	}
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/composition/definitions", compositionSaveRequest(a, three.ID, one.ID, 2)))
	store := compositionStore(t, a)
	if len(store.Items) != 2 || store.Revision != 3 || store.Items[0].ID != one.ID || store.Items[0].Definition.Name != definition.Name || store.Items[0].CaptureID != three.ID {
		t.Fatal("overwrite changed persistent identity or replaced another entry", store)
	}
	clearComposition(t, a, three.ID)
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/composition/definitions", compositionSaveRequest(a, three.ID, one.ID, 2)))
	deleteRequest := map[string]any{"action": "delete", "id": one.ID, "book": sourceBookID(a.currentWorkbook()), "workbookKey": a.currentWorkbook().Fingerprint, "expectedRevision": 2}
	if response := reconcilePost(t, a, "/api/composition/definitions", deleteRequest); response.Code != 409 {
		t.Fatal("stale delete ignored catalog revision", response.Code)
	}
	deleteRequest["expectedRevision"] = 3
	decodeResponse[map[string]any](t, reconcilePost(t, a, "/api/composition/definitions", deleteRequest))
	if store := compositionStore(t, a); store.Revision != 4 || len(store.Items) != 1 || store.Items[0].ID != two.ID {
		t.Fatal("delete changed unrelated definitions", store)
	}
}

func TestCompositionCatalogKeepsConfirmedFileOnSyncAndRenameFailures(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	path := filepath.Join(t.TempDir(), "catalog.json")
	first := savedComposition{ID: strings.Repeat("a", 24), CaptureID: strings.Repeat("a", 24), Definition: definition}
	if _, err := saveCompositionDefinition(path, first, 0, false, durableConfigOps); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	second := first
	second.ID, second.CaptureID = strings.Repeat("b", 24), strings.Repeat("b", 24)
	second.Definition, _ = definition.Normalized()
	second.Definition.Name = "新的来源"
	sentinel := errors.New("injected durable config failure")
	for _, ops := range []configWriteOps{
		{sync: func(*os.File) error { return sentinel }, rename: os.Rename},
		{sync: (*os.File).Sync, rename: func(string, string) error { return sentinel }},
	} {
		if _, err := saveCompositionDefinition(path, second, 1, false, ops); !errors.Is(err, sentinel) {
			t.Fatal("durability failure was accepted", err)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
			t.Fatal("failed durable save replaced confirmed definition")
		}
	}
}

func TestCompositionCatalogRejectsCorruptionUnknownVersionsAndDuplicateBindings(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	path := filepath.Join(t.TempDir(), "catalog.json")
	value := savedComposition{ID: strings.Repeat("a", 24), CaptureID: strings.Repeat("a", 24), Definition: definition}
	if _, err := saveCompositionDefinition(path, value, 0, false, durableConfigOps); err != nil {
		t.Fatal(err)
	}
	store, err := readCompositionDefinitions(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*compositionDefinitionStore){
		"future catalog":     func(s *compositionDefinitionStore) { s.Version++ },
		"old descriptor":     func(s *compositionDefinitionStore) { s.Items[0].Definition.Version = 0 },
		"same mapped column": func(s *compositionDefinitionStore) { s.Items[0].Definition.Inputs[0].Columns[1] = 1 },
		"duplicate identity": func(s *compositionDefinitionStore) { s.Items = append(s.Items, s.Items[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := store
			bad.Items = append([]savedComposition(nil), store.Items...)
			bad.Items[0].Definition, _ = definition.Normalized()
			change(&bad)
			if err := writeSavedConfig(path, 8<<20, bad, durableConfigOps); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if _, err := readCompositionDefinitions(path); err == nil {
				t.Fatal("invalid catalog or definition accepted")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
				t.Fatal("invalid catalog was silently reset or rewritten")
			}
		})
	}
}

func TestCompositionCatalogConcurrentWritersDoNotLoseConfirmedDefinitions(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	path := filepath.Join(t.TempDir(), "catalog.json")
	var workers sync.WaitGroup
	errorsFound := make(chan error, 2)
	for _, letter := range []string{"a", "b"} {
		workers.Go(func() {
			copy, _ := definition.Normalized()
			copy.Name += letter
			_, err := saveCompositionDefinition(path, savedComposition{ID: strings.Repeat(letter, 24), CaptureID: strings.Repeat(letter, 24), Definition: copy}, 0, false, durableConfigOps)
			errorsFound <- err
		})
	}
	workers.Wait()
	close(errorsFound)
	conflicts, successes := 0, 0
	for err := range errorsFound {
		if err == nil {
			successes++
		} else if errors.Is(err, errCompositionRevision) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	store, err := readCompositionDefinitions(path)
	if err != nil || successes != 1 || conflicts != 1 || store.Revision != 1 || len(store.Items) != 1 {
		t.Fatal("concurrent catalog writes lost data", store, successes, conflicts, err)
	}
}

func TestCompositionDefinitionSaveRejectsChangedScratchContent(t *testing.T) {
	a, _ := recordFixture(t, 12, "")
	definition := compositionDefinition(t, a)
	wb := a.currentWorkbook()
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: "catalog-scratch", Name: "分析表", Columns: []string{"编号", "金额"}, Rows: [][]string{{"0001", "0.1"}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	definition.Inputs[0].Ref = compositionRef(t, a, sourceBookID(wb), "scratch:catalog-scratch", 0)
	job := compositionStart(t, a, compositionStartRequest{Mode: "append", Definition: &definition})
	waitComposition(t, a, job, "done")
	if err := a.editAnalysis(wb, func(ws *analysis.Workspace) error { ws.Sheet("catalog-scratch").Rows[0][1] = "0.2"; return nil }); err != nil {
		t.Fatal(err)
	}
	if response := reconcilePost(t, a, "/api/composition/definitions", compositionSaveRequest(a, job.ID, "", 0)); response.Code != 409 {
		t.Fatal("saved definition silently rebound changed scratch content", response.Code, response.Body.String())
	}
	if store := compositionStore(t, a); store.Revision != 0 || len(store.Items) != 0 {
		t.Fatal("changed source created a confirmed definition", store)
	}
}
