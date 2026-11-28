package ui

import (
	"bytes"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"sync"

	"excellens/internal/buildinfo"
	"excellens/internal/formats"
	"excellens/internal/i18n"
)

type pageLanguage struct{ Locale i18n.Locale }

func (p pageLanguage) T(key i18n.Key) string { return p.Locale.Text(key) }

type localizedPage struct {
	head string
	body string
}

func (p localizedPage) render(fields ...string) []byte {
	head := strings.NewReplacer(fields...).Replace(p.head)
	page := make([]byte, 0, len(head)+len(p.body))
	page = append(page, head...)
	return append(page, p.body...)
}

func pageLoader(name string, recovery bool) func(i18n.Locale) (localizedPage, error) {
	loaders := make(map[i18n.Locale]func() (localizedPage, error))
	tpl := sync.OnceValues(func() (*template.Template, error) {
		return template.ParseFS(Files, "web/"+name)
	})
	for _, choice := range i18n.Choices() {
		locale := choice.Locale
		loaders[locale] = sync.OnceValues(func() (localizedPage, error) {
			page, err := tpl()
			if err != nil {
				return localizedPage{}, err
			}
			var html bytes.Buffer
			if err := page.Execute(&html, pageLanguage{locale}); err != nil {
				return localizedPage{}, err
			}
			messages, err := locale.BrowserMessages(recovery)
			if err != nil {
				return localizedPage{}, err
			}
			fields := []string{"__LANGUAGE_MESSAGES__", string(messages)}
			if !recovery {
				config, err := indexFormatConfig()
				if err != nil {
					return localizedPage{}, err
				}
				fields = append(fields, "__APP_VERSION__", buildinfo.Version,
					"__MAX_SOURCE_ROWS__", strconv.Itoa(formats.ExternalRows),
					"__FILE_ACCEPT__", formats.Accept(), "__FILE_FORMATS__", config)
			}
			_, urls := assetCatalog()
			// Request-specific fields belong in the head, before the static page body.
			const headEnd = "</head>"
			head, body, found := strings.Cut(urls.Replace(html.String()), headEnd)
			if !found {
				return localizedPage{}, fmt.Errorf("%s: missing page head", name)
			}
			static := strings.NewReplacer(fields...)
			return localizedPage{head: static.Replace(head) + headEnd, body: static.Replace(body)}, nil
		})
	}
	return func(locale i18n.Locale) (localizedPage, error) {
		load := loaders[locale]
		if load == nil {
			load = loaders[i18n.Default]
		}
		return load()
	}
}

var indexPage = pageLoader("index.html", false)
var recoveryPage = pageLoader("storage-recovery.html", true)

// RecoveryIndex has no workbook bootstrap or tab lease.
func RecoveryIndex(token string, locale i18n.Locale) ([]byte, error) {
	page, err := recoveryPage(locale)
	if err != nil {
		return nil, err
	}
	return page.render("__APP_TOKEN__", token), nil
}
