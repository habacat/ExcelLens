package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

//go:embed catalogs/*.json
var files embed.FS

type Key string
type Args map[string]any

type Message struct {
	Key  Key
	Args Args
}

func (m Message) Text(locale Locale) string {
	if m.Key == "" {
		return ""
	}
	return locale.Text(m.Key, m.Args)
}

var catalogs = func() map[Locale]func() map[Key]string {
	result := make(map[Locale]func() map[Key]string, len(choices))
	for _, choice := range choices {
		result[choice.Locale] = sync.OnceValue(func() map[Key]string {
			data, err := files.ReadFile("catalogs/" + string(choice.Locale) + ".json")
			if err != nil {
				panic(err)
			}
			var messages map[Key]string
			if err := json.Unmarshal(data, &messages); err != nil {
				panic(fmt.Errorf("load %s message catalog: %w", choice.Locale, err))
			}
			return messages
		})
	}
	return result
}()

func catalog(locale Locale) map[Key]string {
	if load := catalogs[locale]; load != nil {
		return load()
	}
	return catalogs[Default]()
}

var placeholder = regexp.MustCompile(`\{[a-zA-Z][a-zA-Z0-9]*\}`)

// Text formats one catalog entry. Substituted values are never parsed as
// placeholders; callers choose the escaping appropriate to their output.
func (locale Locale) Text(key Key, args ...Args) string {
	message, ok := catalog(locale)[key]
	if !ok {
		message, ok = catalog(Default)[key]
		if !ok {
			return string(key)
		}
	}
	if len(args) == 0 {
		return message
	}
	return placeholder.ReplaceAllStringFunc(message, func(token string) string {
		if value, ok := args[0][token[1:len(token)-1]]; ok {
			return fmt.Sprint(value)
		}
		return token
	})
}

// Stage renders the application stage IDs stored with jobs. Old history can
// still contain previously recorded prose, which is shown as stored.
func (locale Locale) Stage(value string) string {
	if strings.HasPrefix(value, "stage.") {
		return locale.Text(Key(value))
	}
	return value
}

// BrowserMessages supplies only the catalog entries used by the current page.
func (locale Locale) BrowserMessages(recovery bool) ([]byte, error) {
	messages := make(map[Key]string)
	for key, value := range catalog(locale) {
		name := string(key)
		if !strings.HasPrefix(name, "ui.") {
			continue
		}
		if !recovery || strings.HasPrefix(name, "ui.storageRecovery.") || strings.HasPrefix(name, "ui.common.") {
			messages[key] = value
		}
	}
	return json.Marshal(messages)
}
