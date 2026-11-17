package reconcile

import (
	"bytes"
	"context"
	"encoding/csv"
	"strings"
	"testing"
)

func TestCSVStreamsAllDuplicateRowsAndLabelsProtectedText(t *testing.T) {
	left := fixtureSource([]Value{text("d"), text("=LEFT")}, []Value{text("d"), text("多\n行")}, []Value{text("left"), text("000123")})
	right := fixtureSource([]Value{text("d"), text("=RIGHT")}, []Value{text("d"), text("末行")})
	query := Query{Keys: []Pair{{Left: 1, Right: 1}}, Fields: []Pair{{Left: 2, Right: 2}}}
	for _, protected := range []bool{false, true} {
		var out bytes.Buffer
		var protect func(string) string
		if protected {
			protect = func(text string) string {
				if strings.HasPrefix(text, "=") {
					return "'" + text
				}
				return text
			}
		}
		writer, err := NewCSVWriter(&out, query, left.Info, right.Info, protect)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Run(context.Background(), t.TempDir(), query, left, right, writer.Sinks(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(&out).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 7 || writer.Rows != 6 || result.Counts.Duplicate != 1 {
			t.Fatal("duplicate rows expanded or lost", len(rows), result.Counts)
		}
		prefix := ""
		if protected {
			prefix = "'"
			if !strings.Contains(rows[0][13], "文本保护") {
				t.Fatal("protection was not labelled")
			}
		}
		var found, unicode, identifier bool
		for _, row := range rows[1:] {
			found = found || row[13] == prefix+"=LEFT"
			unicode = unicode || row[13] == "多\n行"
			identifier = identifier || row[13] == "000123"
		}
		if !found || !unicode || !identifier {
			t.Fatal("CSV literal fidelity", rows)
		}
	}
}

func TestUnknownSourceTypeRequiresAnExplicitModeAndPlaceholderNeverMatches(t *testing.T) {
	unknown := text("123")
	unknown.TypeMissing = true
	left, right := fixtureSource([]Value{unknown, text("2")}), fixtureSource([]Value{text("123"), text("2")})
	for _, mode := range []string{"", "text", "number"} {
		query := Query{Keys: []Pair{{Left: 1, Right: 1, Mode: mode}}}
		result, err := Run(context.Background(), t.TempDir(), query, left, right, Sinks{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "" && result.Counts.InvalidKeys != 1 || mode != "" && result.Counts.Equal != 1 {
			t.Fatal("unknown original type was silently promoted", mode, result.Counts)
		}
	}
	placeholder := text("[NULL]")
	placeholder.TypeMissing = true
	placeholder.Uncertain = "缓存占位无法对应原类型"
	for _, mode := range []string{"", "text", "number"} {
		result, err := Run(context.Background(), t.TempDir(), Query{Keys: []Pair{{Left: 1, Right: 1, Mode: mode}}}, fixtureSource([]Value{placeholder}), fixtureSource([]Value{text("[NULL]")}), Sinks{}, nil)
		invalid := int64(1)
		if mode == "number" {
			invalid = 2 // the literal text on the other side is not a number either
		}
		if err != nil || result.Counts.Equal != 0 || result.Counts.InvalidKeys != invalid {
			t.Fatal("placeholder treated as literal source text", mode, result.Counts, err)
		}
	}
}
