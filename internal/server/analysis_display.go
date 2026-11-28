package server

import (
	"io"
	"net/http"

	"excellens/internal/i18n"
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
		return config, i18n.Error("service.analysisDisplay.analysisDisplaySettingsInvalidOriginal")
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
			writeMessage(w, 400, "service.analysisDisplay.provideCompleteAnalysisDisplaySettings")
			return
		}
	}
	path, err := savedConfigPath("settings", "analysis-display")
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	config, err := readAnalysisDisplay(path)
	if err != nil {
		writeFailure(w, 422, err)
		return
	}
	if r.Method == http.MethodPost {
		if config.Revision != *req.ExpectedRevision {
			writeMessage(w, 409, "service.analysisDisplay.settingsHaveModifiedAnotherPage")
			return
		}
		if config.Revision+1 >= maxConfigRevision {
			writeMessage(w, 409, "service.analysisDisplay.revisionNumberSettingReachedLimit")
			return
		}
		config.Revision++
		config.Options = options
		if err := writeSavedConfig(path, analysisDisplayBytes, config, durableConfigOps); err != nil {
			writeMessage(w, 500, "service.analysisDisplay.failedSaveDisplaySettings", i18n.Args{"detail": err})
			return
		}
	}
	writeJSON(w, config)
}
