package server

import (
	"errors"
	"io"
	"net/http"
)

const analysisDisplayBytes = 4096

type analysisDisplayOptions struct {
	GroupThousands     bool `json:"groupThousands"`
	Scientific         bool `json:"scientific"`
	ScientificExponent int  `json:"scientificExponent"`
}

type analysisDisplayConfig struct {
	Version  int                    `json:"version"`
	Revision int64                  `json:"revision"`
	Options  analysisDisplayOptions `json:"options"`
}

// Pointers distinguish a deliberate false from a missing or null setting.
type analysisDisplayInput struct {
	GroupThousands     *bool `json:"groupThousands"`
	Scientific         *bool `json:"scientific"`
	ScientificExponent int   `json:"scientificExponent"`
}

func (input *analysisDisplayInput) options() (analysisDisplayOptions, bool) {
	if input == nil || input.GroupThousands == nil || input.Scientific == nil ||
		(input.ScientificExponent != 9 && input.ScientificExponent != 12 && input.ScientificExponent != 15) {
		return analysisDisplayOptions{}, false
	}
	return analysisDisplayOptions{*input.GroupThousands, *input.Scientific, input.ScientificExponent}, true
}

func readAnalysisDisplay(path string) (analysisDisplayConfig, error) {
	config := analysisDisplayConfig{Version: 1, Options: analysisDisplayOptions{true, true, 12}}
	var input struct {
		Version  int                   `json:"version"`
		Revision *int64                `json:"revision"`
		Options  *analysisDisplayInput `json:"options"`
	}
	found, err := readSavedConfig(path, analysisDisplayBytes, &input)
	if err != nil || !found {
		return config, err
	}
	options, valid := input.Options.options()
	if input.Version != 1 || input.Revision == nil || *input.Revision < 0 || *input.Revision >= maxConfigRevision || !valid {
		return config, errors.New("分析显示设置无效，原文件已保留")
	}
	config.Revision, config.Options = *input.Revision, options
	return config, nil
}

// Global user preferences do not belong to a workbook, browser origin, cache
// or saved query. Each transaction shares the durable config lock across tabs.
func (a *App) handleAnalysisDisplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ExpectedRevision *int64                `json:"expectedRevision"`
		Options          *analysisDisplayInput `json:"options"`
	}
	var options analysisDisplayOptions
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(io.LimitReader(r.Body, analysisDisplayBytes+1))
		var valid bool
		if err == nil && len(data) <= analysisDisplayBytes && decodeSavedConfig(data, &req) == nil {
			options, valid = req.Options.options()
		}
		if !valid || req.ExpectedRevision == nil || *req.ExpectedRevision < 0 || *req.ExpectedRevision >= maxConfigRevision {
			writeError(w, 400, "请完整提供分析显示设置和有效修订号")
			return
		}
	}
	path, err := savedConfigPath("settings", "analysis-display")
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	config, err := readAnalysisDisplay(path)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if r.Method == http.MethodPost {
		if config.Revision != *req.ExpectedRevision {
			writeError(w, 409, "设置已被其他页面修改，请重新载入后重试")
			return
		}
		if config.Revision+1 >= maxConfigRevision {
			writeError(w, 409, "设置修订号已达到上限，原文件已保留")
			return
		}
		config.Revision++
		config.Options = options
		if err := writeSavedConfig(path, analysisDisplayBytes, config, durableConfigOps); err != nil {
			writeError(w, 500, "保存显示设置失败："+err.Error())
			return
		}
	}
	writeJSON(w, config)
}
