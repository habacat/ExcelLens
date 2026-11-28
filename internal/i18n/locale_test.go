package i18n

import "testing"

func TestLanguageNegotiation(t *testing.T) {
	for _, test := range []struct {
		header string
		want   Locale
	}{
		{"zh-CN", "zh-Hans"},
		{"zh-HK,zh;q=0.8", "zh-Hant"},
		{"zh-Hant-TW", "zh-Hant"},
		{"ja-JP,en;q=0.7", "ja"},
		{"ko-KR", "ko"},
		{"ru-RU,en;q=0.3", "ru"},
		{"fr-FR,de-DE;q=0.9,en-GB;q=0.5", "en"},
		{"zh;q=0,en-US;q=0.2", "en"},
		{"fr-FR,*;q=0.3", "ko"},
		{"", "ko"},
	} {
		if got := Match(test.header, "ko"); got != test.want {
			t.Errorf("Match(%q) = %q, want %q", test.header, got, test.want)
		}
	}
}
