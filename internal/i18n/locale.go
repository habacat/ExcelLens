// Package i18n owns the application's supported languages and message catalogs.
package i18n

import (
	"strings"
	"sync"

	"golang.org/x/text/language"
)

type Locale string

const Default Locale = "zh-Hans"

type Choice struct {
	Locale Locale
	Name   string
}

var choices = []Choice{
	{Default, "简体中文"},
	{"zh-Hant", "繁體中文"},
	{"en", "English"},
	{"ja", "日本語"},
	{"ko", "한국어"},
	{"ru", "Русский"},
}

func Choices() []Choice {
	return append([]Choice(nil), choices...)
}

func Supported(value string) bool {
	for _, choice := range choices {
		if value == string(choice.Locale) {
			return true
		}
	}
	return false
}

var matcher = func() language.Matcher {
	tags := make([]language.Tag, 0, len(choices))
	for _, choice := range choices {
		tags = append(tags, language.MustParse(string(choice.Locale)))
	}
	return language.NewMatcher(tags, language.PreferSameScript(true))
}()

// Match selects a supported language in Accept-Language priority order.
func Match(header string, fallback Locale) Locale {
	tags, _, err := language.ParseAcceptLanguage(header)
	if err == nil {
		for _, tag := range tags {
			_, index, confidence := matcher.Match(tag)
			if confidence >= language.High {
				return choices[index].Locale
			}
		}
	}
	return fallback
}

var System = sync.OnceValue(func() Locale {
	return Match(strings.Join(systemLanguages(), ","), Default)
})
