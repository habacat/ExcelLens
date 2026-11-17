package workbook

import (
	"bytes"
	"strings"
)

// Value-only path for ordinary Excel-generated inline runs. It deliberately
// understands no font semantics: a flat rPr may contain only well-formed
// empty tags and simple XML attributes. Nested metadata, entities, namespaces,
// comments, CDATA or mixed text return false for the shared XML reference.
// The opt-in presentation reader always decodes the real font properties.
func fastSimpleRichContent(part []byte) (string, bool) {
	part = bytes.TrimSpace(part)
	var value strings.Builder
	found := false
	for len(part) > 0 {
		if !bytes.HasPrefix(part, []byte("<r>")) {
			return "", false
		}
		end := bytes.Index(part[len("<r>"):], []byte("</r>"))
		if end < 0 {
			return "", false
		}
		end += len("<r>")
		body := bytes.TrimSpace(part[len("<r>"):end])
		if bytes.HasPrefix(body, []byte("<rPr>")) {
			close := bytes.Index(body, []byte("</rPr>"))
			if close < 0 || !fastFlatRichProperties(body[len("<rPr>"):close]) {
				return "", false
			}
			body = bytes.TrimSpace(body[close+len("</rPr>"):])
		}
		text, ok := fastSimpleTextBytes(body)
		if !ok {
			return "", false
		}
		value.Write(text)
		found = true
		part = bytes.TrimSpace(part[end+len("</r>"):])
	}
	return value.String(), found
}

func fastFlatRichProperties(part []byte) bool {
	for part = bytes.TrimSpace(part); len(part) > 0; part = bytes.TrimSpace(part) {
		end := fastTagEnd(part, 0)
		if end < 0 || !fastEmptyRichProperty(part[:end+1]) {
			return false
		}
		part = part[end+1:]
	}
	return true
}

func fastXMLNameByte(c byte, first bool) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || !first && (c >= '0' && c <= '9' || c == '-' || c == '.')
}

func fastEmptyRichProperty(tag []byte) bool {
	if len(tag) < 4 || tag[0] != '<' || !bytes.HasSuffix(tag, []byte("/>")) || !fastXMLNameByte(tag[1], true) {
		return false
	}
	last, pos := len(tag)-2, 2
	for pos < last && fastXMLNameByte(tag[pos], false) {
		pos++
	}
	for pos < last {
		if !isXMLSpace(tag[pos]) {
			return false
		}
		for pos < last && isXMLSpace(tag[pos]) {
			pos++
		}
		if pos == last {
			return true
		}
		if !fastXMLNameByte(tag[pos], true) {
			return false
		}
		pos++
		for pos < last && fastXMLNameByte(tag[pos], false) {
			pos++
		}
		for pos < last && isXMLSpace(tag[pos]) {
			pos++
		}
		if pos == last || tag[pos] != '=' {
			return false
		}
		pos++
		for pos < last && isXMLSpace(tag[pos]) {
			pos++
		}
		if pos == last || tag[pos] != '\'' && tag[pos] != '"' {
			return false
		}
		quote := tag[pos]
		pos++
		start := pos
		for pos < last && tag[pos] != quote {
			pos++
		}
		if pos == last || !fastXMLTextBytes(tag[start:pos]) {
			return false
		}
		pos++
	}
	return true
}
