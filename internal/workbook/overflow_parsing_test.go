package workbook

import "testing"

func TestIntegerParsingRejectsOverflow(t *testing.T) {
	for _, input := range []string{"9999999999999999999999999999999999999999999999999999", "18446744073709551616", "-1", "12x"} {
		if _, err := parseNonNegativeInt(input); err == nil {
			t.Fatalf("invalid integer %q accepted", input)
		}
	}
	for _, ref := range []string{"ZZZZZZZZZZZZZZ999", "AAAAAA1"} {
		if got := parseColumnBytes([]byte(ref)); got != 0 {
			t.Fatalf("bad huge column %q parsed as %d", ref, got)
		}
	}
}
