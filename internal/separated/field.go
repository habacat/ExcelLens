// Package separated writes field text without changing embedded line endings.
package separated

import (
	"bufio"
	"strings"
	"unicode"
	"unicode/utf8"
)

func ProtectText(value string) string {
	s := strings.TrimLeft(value, "\t\r\n ")
	if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
		return "'" + value
	}
	return value
}

func WriteField(out *bufio.Writer, field string, comma byte, forceQuote bool) error {
	first, _ := utf8.DecodeRuneInString(field)
	quoted := forceQuote || strings.IndexByte(field, comma) >= 0 || strings.ContainsAny(field, "\"\r\n") || unicode.IsSpace(first) || field == `\.`
	if !quoted {
		_, err := out.WriteString(field)
		return err
	}
	if err := out.WriteByte('"'); err != nil {
		return err
	}
	for {
		at := strings.IndexByte(field, '"')
		if at < 0 {
			if _, err := out.WriteString(field); err != nil {
				return err
			}
			break
		}
		if _, err := out.WriteString(field[:at+1]); err != nil {
			return err
		}
		if err := out.WriteByte('"'); err != nil {
			return err
		}
		field = field[at+1:]
	}
	return out.WriteByte('"')
}
