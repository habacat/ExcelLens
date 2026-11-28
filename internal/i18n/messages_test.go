package i18n

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestCatalogsAndParameters(t *testing.T) {
	base := catalog(Default)
	for _, choice := range Choices() {
		t.Run(string(choice.Locale), func(t *testing.T) {
			messages := catalog(choice.Locale)
			if len(messages) != len(base) {
				t.Fatalf("catalog has %d messages, want %d", len(messages), len(base))
			}
			names := func(text string) []string {
				matches := placeholder.FindAllString(text, -1)
				slices.Sort(matches)
				return slices.Compact(matches)
			}
			for key, original := range base {
				message := messages[key]
				if strings.TrimSpace(message) == "" || !slices.Equal(names(message), names(original)) {
					t.Errorf("missing message or incorrect placeholders for %s", key)
				}
			}
		})
	}
}

func TestLocalizedErrorPreservesCauseAndLiteralParameter(t *testing.T) {
	cause := errors.New("diagnostic <file> {detail}")
	err := Wrap("service.language.saveFailed", cause)
	if !errors.Is(err, cause) || ErrorKey(err) != "service.language.saveFailed" {
		t.Fatal("wrapping lost the error identity")
	}
	const want = "Could not save the interface language: diagnostic <file> {detail}"
	if got := Locale("en").ErrorText(err); got != want {
		t.Fatalf("ErrorText = %q, want %q", got, want)
	}
}
