package server

import (
	"excellens/internal/calculator"
	"excellens/internal/valueconv"
)

type conversionSuggestion struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Preview string `json:"preview"`
}

func calculationSuggestions(result calculator.Result) []conversionSuggestion {
	if result.Kind != "text" {
		return nil
	}
	kind, preview := valueconv.Suggest(result.Value)
	if kind == "" {
		return nil
	}
	label := map[string]string{"date": "转日期数值", "time": "转时间数值", "datetime": "转日期时间数值"}[kind]
	return []conversionSuggestion{{Kind: kind, Label: label, Preview: preview}}
}
