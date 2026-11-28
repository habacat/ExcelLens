//go:build !windows

package i18n

import (
	"os"
	"strings"
)

func systemLanguages() []string {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANGUAGE", "LANG"} {
		if value := os.Getenv(name); value != "" {
			tags := strings.Split(value, ":")
			for i, tag := range tags {
				end := strings.IndexAny(tag, ".@")
				if end >= 0 {
					tag = tag[:end]
				}
				tags[i] = strings.ReplaceAll(tag, "_", "-")
			}
			return tags
		}
	}
	return nil
}
