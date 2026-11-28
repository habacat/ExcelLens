package server

import (
	"encoding/json"

	"net/http"
	"reflect"
	"strings"

	"excellens/internal/i18n"
	"excellens/internal/recipe"
)

const maxRecipes = 64
const maxRecipeBytes = 256 << 10
const maxRecipesBytes = 4 << 20
const recipeKnownTypes = recipeNumber | recipeText | recipeAmountText | recipeBoolean | recipeDate | recipeError | recipeMissingCache | recipeInvalidNumber | recipeUnknown | recipeTypeMissing

var errRecipeRevision = i18n.Error("service.recipesStore.analysisPlanDirectoryModifiedOn")
var errRecipeName = i18n.Error("service.recipesStore.thereAlreadyAnalysisSchemeSame")

type recipeStore struct {
	Version  int             `json:"version"`
	Revision int64           `json:"revision"`
	Items    []recipe.Recipe `json:"items"`
}

func recipesPath() (string, error) { return savedConfigPath("recipes", "catalog") }

func validateSavedRecipe(value recipe.Recipe) error {
	if !validSessionID(value.ID) || !recipe.ValidName(value.Name) {
		return i18n.Error("service.recipesStore.analysisSchemeIdentifierOrName")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxRecipeBytes {
		return i18n.Error("service.recipesStore.analysisPlanExceedsKibConfiguration")
	}
	mapping := make(map[string]string, len(value.Fields))
	for _, field := range value.Fields {
		mask := recipeTypeMask(field.Mask)
		if mask == 0 || mask&^recipeKnownTypes != 0 || field.Uncertain != mask.uncertain() {
			return i18n.Error("service.recipesStore.invalidReadingTypeAnalyzedScheme")
		}
		mapping[field.ID] = field.ID
	}
	_, _, err = value.Query(value.Origin.Source, value.Origin.HeaderRow, mapping)
	return err
}

func readRecipes(path string) (recipeStore, error) {
	store := recipeStore{Version: 1, Items: []recipe.Recipe{}}
	found, err := readSavedConfig(path, maxRecipesBytes, &store)
	if err != nil || !found {
		return store, err
	}
	if store.Version != 1 || store.Revision < 0 || store.Revision >= maxConfigRevision || len(store.Items) > maxRecipes {
		return store, i18n.Error("service.recipesStore.versionRevisionOrQuantityAnalysis")
	}
	ids, names := make(map[string]bool), make(map[string]bool)
	for _, value := range store.Items {
		name := strings.ToLower(value.Name)
		if ids[value.ID] || names[name] {
			return store, i18n.Error("service.recipesStore.thereDuplicatesOrIdenticalNames")
		}
		if err := validateSavedRecipe(value); err != nil {
			return store, i18n.Wrap("service.recipesStore.analysisSchemeDirectoryStructureInvalid", err)
		}
		ids[value.ID], names[name] = true, true
	}
	return store, nil
}

func saveCapturedRecipe(value recipe.Recipe, revision int64) (int64, error) {
	if err := validateSavedRecipe(value); err != nil {
		return 0, err
	}
	path, err := recipesPath()
	if err != nil {
		return 0, err
	}
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	store, err := readRecipes(path)
	if err != nil {
		return 0, err
	}
	for _, old := range store.Items {
		if old.ID == value.ID {
			if reflect.DeepEqual(old, value) {
				return store.Revision, nil
			}
			return 0, i18n.Error("service.recipesStore.thisSaveRequestCompleteCannot")
		}
	}
	if store.Revision != revision {
		return 0, errRecipeRevision
	}
	if len(store.Items) >= maxRecipes {
		return 0, i18n.Error("service.recipesStore.onlyAnalysisPlansCanBe")
	}
	for _, old := range store.Items {
		if strings.EqualFold(old.Name, value.Name) {
			return 0, errRecipeName
		}
	}
	if store.Revision+1 >= maxConfigRevision {
		return 0, i18n.Error("service.recipesStore.analysisPlanRevisionNumberReached")
	}
	store.Items = append(store.Items, value)
	store.Revision++
	if err := writeSavedConfig(path, maxRecipesBytes, store, durableConfigOps); err != nil {
		return 0, err
	}
	return store.Revision, nil
}

func findRecipe(id string) (recipe.Recipe, error) {
	path, err := recipesPath()
	if err != nil {
		return recipe.Recipe{}, err
	}
	lock := persistentFileLock(path)
	lock.RLock()
	defer lock.RUnlock()
	store, err := readRecipes(path)
	if err != nil {
		return recipe.Recipe{}, err
	}
	for _, value := range store.Items {
		if value.ID == id {
			return value, nil
		}
	}
	return recipe.Recipe{}, i18n.Error("service.recipesStore.analysisPlanRemovedRefreshDirectory")
}

func (a *App) handleRecipes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	path, err := recipesPath()
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	if r.Method == http.MethodGet {
		lock := persistentFileLock(path)
		lock.RLock()
		store, err := readRecipes(path)
		lock.RUnlock()
		if err != nil {
			writeFailure(w, 422, err)
			return
		}
		writeJSON(w, map[string]any{"book": sourceBookID(a.currentWorkbook()), "store": store})
		return
	}
	var req struct {
		Action           string `json:"action"`
		ID               string `json:"id"`
		ExpectedRevision *int64 `json:"expectedRevision"`
	}
	if decodeAnalysis(r, &req) != nil || req.Action != "delete" || !validSessionID(req.ID) || req.ExpectedRevision == nil {
		writeMessage(w, 400, "service.recipesStore.analysisPlanDirectoryOperationsInvalid")
		return
	}
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	store, err := readRecipes(path)
	if err != nil {
		writeFailure(w, 422, err)
		return
	}
	if store.Revision != *req.ExpectedRevision {
		writeMessage(w, 409, "service.recipesStore.analysisSchemeDirectoryModifiedAnother")
		return
	}
	found := -1
	for i, value := range store.Items {
		if value.ID == req.ID {
			found = i
			break
		}
	}
	if found < 0 {
		writeMessage(w, 404, "service.recipesStore.analysisPlanRemovedRefreshDirectory")
		return
	}
	if store.Revision+1 >= maxConfigRevision {
		writeMessage(w, 409, "service.recipesStore.analysisPlanRevisionNumberReached")
		return
	}
	store.Items = append(store.Items[:found], store.Items[found+1:]...)
	store.Revision++
	if err := writeSavedConfig(path, maxRecipesBytes, store, durableConfigOps); err != nil {
		writeMessage(w, 500, "service.recipesStore.failedDeleteAnalyzedScheme", i18n.Args{"detail": err})
		return
	}
	writeJSON(w, map[string]any{"book": sourceBookID(a.currentWorkbook()), "store": store})
}
