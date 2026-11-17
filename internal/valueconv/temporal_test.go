package valueconv

import "testing"

func TestExplicitTemporalConversion(t *testing.T) {
	for _, tc := range []struct{ raw, kind, input, want string }{
		{"2026/1/2", "date", "auto", "2026-01-02"},
		{"2026年11月11日", "date", "auto", "2026-11-11"},
		{"2026.11.11 09:05", "datetime", "auto", "2026-11-11 09:05:00"},
		{"2026-11-11T09:05:12.123456789", "datetime", "auto", "2026-11-11 09:05:12.123456789"},
		{"09:05", "time", "auto", "09:05:00"},
		{"09:05:12.5", "time", "auto", "09:05:12.5"},
		{"11/12/2026", "date", "dmy", "2026-12-11"},
		{"11/12/2026", "date", "mdy", "2026-11-12"},
		{"20261111", "date", "compact", "2026-11-11"},
		{"2024-2-29", "date", "auto", "2024-02-29"},
	} {
		t.Run(tc.raw+"/"+tc.input, func(t *testing.T) {
			got, err := Convert(tc.raw, tc.kind, tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("converted=%q, want %q: %v", got, tc.want, err)
			}
		})
	}
	for _, raw := range []string{"2026-2-29", "2026-11-31", "24:00", "09:60", "0000-01-01", "01/02/2026", "20261111", "00001234", "2026-11-11T09:00:00+08:00"} {
		if kind, _ := Suggest(raw); kind != "" {
			t.Fatalf("ambiguous, invalid or unrequested input received a hint: %q", raw)
		}
	}
	if _, err := Convert("2026-11-11", "datetime", "auto"); err == nil {
		t.Fatal("a missing time must not be invented")
	}
	if _, err := Convert("09:00", "date", "auto"); err == nil {
		t.Fatal("a missing date must not be invented")
	}
}

func BenchmarkSuggestionUnrelatedText(b *testing.B) {
	for b.Loop() {
		Suggest("普通账簿摘要及前导零编号0000123456789")
	}
}

func BenchmarkSuggestionDate(b *testing.B) {
	for b.Loop() {
		Suggest("2026/1/2 09:05:12.5")
	}
}
