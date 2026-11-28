package server

import (
	"excellens/internal/calculator"
	"excellens/internal/i18n"
	"excellens/internal/valueconv"
)

type conversionSuggestion struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Preview string `json:"preview"`
}

var conversionLabels = map[string]i18n.Key{
	"date":     "service.conversion.toDate",
	"time":     "service.conversion.toTime",
	"datetime": "service.conversion.toDateTime",
}

func calculationSuggestions(result calculator.Result, locale i18n.Locale) []conversionSuggestion {
	if result.Kind != "text" {
		return nil
	}
	kind, preview := valueconv.Suggest(result.Value)
	if kind == "" {
		return nil
	}
	return []conversionSuggestion{{Kind: kind, Label: locale.Text(conversionLabels[kind]), Preview: preview}}
}
