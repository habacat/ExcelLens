package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"excellens/internal/recipe"
)

const maxRecipes = 64
const maxRecipeBytes = 256 << 10
const maxRecipesBytes = 4 << 20
const recipeKnownTypes = recipeNumber | recipeText | recipeAmountText | recipeBoolean | recipeDate | recipeError | recipeMissingCache | recipeInvalidNumber | recipeUnknown | recipeTypeMissing

var errRecipeRevision = errors.New("分析方案目录已被其他页面修改，请刷新后重试保存")
var errRecipeName = errors.New("已有同名分析方案，请更换名称")

type recipeStore struct {
	Version  int             `json:"version"`
	Revision int64           `json:"revision"`
	Items    []recipe.Recipe `json:"items"`
}

func recipesPath() (string, error) { return savedConfigPath("recipes", "catalog") }

func validateSavedRecipe(value recipe.Recipe) error {
	if !validSessionID(value.ID) || !recipe.ValidName(value.Name) {
		return errors.New("分析方案标识或名称无效")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxRecipeBytes {
		return errors.New("分析方案超过256KiB配置容量")
	}
	mapping := make(map[string]string, len(value.Fields))
	for _, field := range value.Fields {
		mask := recipeTypeMask(field.Mask)
		if mask == 0 || mask&^recipeKnownTypes != 0 || field.Uncertain != mask.uncertain() {
			return errors.New("分析方案字段读取类型无效")
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
		return store, errors.New("分析方案目录版本、修订或数量无效，原文件已保留")
	}
	ids, names := make(map[string]bool), make(map[string]bool)
	for _, value := range store.Items {
		name := strings.ToLower(value.Name)
		if ids[value.ID] || names[name] {
			return store, errors.New("分析方案目录存在重复标识或名称，原文件已保留")
		}
		if err := validateSavedRecipe(value); err != nil {
			return store, fmt.Errorf("分析方案目录结构无效，原文件已保留：%w", err)
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
			return 0, errors.New("此保存请求已完成，不能用不同内容重试")
		}
	}
	if store.Revision != revision {
		return 0, errRecipeRevision
	}
	if len(store.Items) >= maxRecipes {
		return 0, errors.New("最多保存64个分析方案，请先删除不用的分析方案")
	}
	for _, old := range store.Items {
		if strings.EqualFold(old.Name, value.Name) {
			return 0, errRecipeName
		}
	}
	if store.Revision+1 >= maxConfigRevision {
		return 0, errors.New("分析方案修订号达到上限，原文件已保留")
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
	return recipe.Recipe{}, errors.New("分析方案已移除，请刷新目录")
}

func (a *App) handleRecipes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	path, err := recipesPath()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		lock := persistentFileLock(path)
		lock.RLock()
		store, err := readRecipes(path)
		lock.RUnlock()
		if err != nil {
			writeError(w, 422, err.Error())
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
		writeError(w, 400, "分析方案目录操作无效")
		return
	}
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	store, err := readRecipes(path)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if store.Revision != *req.ExpectedRevision {
		writeError(w, 409, "分析方案目录已被其他页面修改，请刷新后重试")
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
		writeError(w, 404, "分析方案已移除，请刷新目录")
		return
	}
	if store.Revision+1 >= maxConfigRevision {
		writeError(w, 409, "分析方案修订号达到上限，原文件已保留")
		return
	}
	store.Items = append(store.Items[:found], store.Items[found+1:]...)
	store.Revision++
	if err := writeSavedConfig(path, maxRecipesBytes, store, durableConfigOps); err != nil {
		writeError(w, 500, "删除分析方案失败："+err.Error())
		return
	}
	writeJSON(w, map[string]any{"book": sourceBookID(a.currentWorkbook()), "store": store})
}
