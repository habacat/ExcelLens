package workbook

import (
	"bytes"
	"encoding/xml"
	"io"
	"unicode/utf8"
)

// decodeSharedStringsFast handles bounded UTF-8 shared-string parts without
// allocating a decoder and token objects for each of the tens of thousands of
// ordinary <si><t>value</t></si> entries. It returns ok=false whenever the
// layout might be ambiguous: the existing strict streaming XML parser is the
// reference and the mandatory fallback. Complex rich text uses the same
// sharedStringItem decoder as before, so phonetic annotations remain excluded.
func decodeSharedStringsFast(data []byte) (values []string, ok bool) {
	return decodeSharedStringsFastWithPresentation(data, nil)
}

func decodeSharedStringsFastWithPresentation(data []byte, presentation *sharedStringPresentation) (values []string, ok bool) {
	if !utf8.Valid(data) || bytes.Contains(data, []byte("<!")) || bytes.Contains(data, []byte("<si:")) || bytes.Contains(data, []byte("</si:")) {
		return nil, false
	}
	text := bytes.TrimSpace(data)
	if bytes.HasPrefix(text, []byte("<?xml")) {
		end := bytes.Index(text, []byte("?>"))
		if end < 0 {
			return nil, false
		}
		// The reference decoder accepts only UTF-8 and XML 1.0. Never
		// silently interpret non-UTF-8 bytes or an unsupported declaration.
		declaration := bytes.ToLower(text[:end])
		if !bytes.Contains(declaration, []byte(`version="1.0"`)) && !bytes.Contains(declaration, []byte(`version='1.0'`)) {
			return nil, false
		}
		if bytes.Contains(declaration, []byte("encoding")) && !bytes.Contains(declaration, []byte("utf-8")) {
			return nil, false
		}
		text = bytes.TrimSpace(text[end+2:])
	}
	if !bytes.HasPrefix(text, []byte("<sst")) || len(text) == 4 || !isXMLSpace(text[4]) && text[4] != '>' {
		return nil, false
	}
	opening := fastTagEnd(text, 0)
	if opening < 0 {
		return nil, false
	}
	// A literal '<' inside a quoted root attribute is invalid XML. The
	// strict reference parser must reject it rather than fast-path it.
	if bytes.IndexByte(text[1:opening], '<') >= 0 {
		return nil, false
	}
	text = bytes.TrimSpace(text[opening+1:])
	result := make([]string, 0, 4096)
	for len(text) > 0 {
		if bytes.HasPrefix(text, []byte("</sst>")) {
			if len(bytes.TrimSpace(text[len("</sst>"):])) != 0 {
				return nil, false
			}
			return result, true
		}
		// Other sst children, e.g. extension lists, are legal but not worth a
		// second homegrown structural grammar. Delegate the entire part to XML.
		if !bytes.HasPrefix(text, []byte("<si>")) {
			return nil, false
		}
		stop := bytes.Index(text, []byte("</si>"))
		if stop < 0 {
			return nil, false
		}
		end := stop + len("</si>")
		part := text[:end]
		if v, simple := fastSimpleSharedItem(part); simple {
			result = append(result, v)
		} else {
			dec := xml.NewDecoder(bytes.NewReader(part))
			first, e := dec.Token()
			if e != nil {
				return nil, false
			}
			start, yes := first.(xml.StartElement)
			if !yes || start.Name.Local != "si" {
				return nil, false
			}
			var value string
			var rich *RichText
			if presentation != nil {
				value, rich, e = presentation.read(dec, start)
			} else {
				var item sharedStringItem
				e = dec.DecodeElement(&item, &start)
				value = item.Text
			}
			if e != nil {
				return nil, false
			}
			if _, e = dec.Token(); e != io.EOF {
				return nil, false
			}
			if presentation != nil && rich != nil {
				presentation.observe(len(result), value, rich)
			}
			result = append(result, value)
		}
		text = bytes.TrimSpace(text[end:])
	}
	return nil, false
}

// Recognizes only one direct text child. Character references, newlines,
// unexpected markup, invalid XML control bytes and rich text ALWAYS go
// through the strict decoder rather than a potentially divergent HTML entity
// unescaper. Most Excel-generated shared strings meet this fast contract.
func fastSimpleSharedItem(part []byte) (string, bool) {
	if !bytes.HasPrefix(part, []byte("<si>")) || !bytes.HasSuffix(part, []byte("</si>")) {
		return "", false
	}
	return fastSimpleTextContent(part[len("<si>") : len(part)-len("</si>")])
}

func fastSimpleTextContent(part []byte) (string, bool) {
	contents, ok := fastSimpleTextBytes(part)
	return string(contents), ok
}

func fastSimpleTextBytes(part []byte) ([]byte, bool) {
	const begin = "<t"
	if !bytes.HasPrefix(part, []byte(begin)) {
		return nil, false
	}
	at := len(begin)
	if at >= len(part) {
		return nil, false
	}
	if part[at] != '>' {
		if !isXMLSpace(part[at]) {
			return nil, false
		}
		end := fastTagEnd(part, 0)
		if end < 0 {
			return nil, false
		}
		// Only xml:space is supported on the fast path; all other attributes
		// remain supported via the strict parser.
		header := part[:end+1]
		if !bytes.Equal(header, []byte(`<t xml:space="preserve">`)) && !bytes.Equal(header, []byte(`<t xml:space='preserve'>`)) && !bytes.Equal(header, []byte(`<t xml:space="default">`)) && !bytes.Equal(header, []byte(`<t xml:space='default'>`)) {
			return nil, false
		}
		at = end
	}
	const tail = "</t>"
	if !bytes.HasSuffix(part, []byte(tail)) {
		return nil, false
	}
	contents := part[at+1 : len(part)-len(tail)]
	if !fastXMLTextBytes(contents) {
		return nil, false
	}
	return contents, true
}

func fastXMLTextBytes(contents []byte) bool {
	if bytes.IndexAny(contents, "<&\r") >= 0 || bytes.Contains(contents, []byte("]]>")) {
		return false
	}
	for _, c := range contents {
		if c < 32 && c != 9 && c != 10 {
			return false
		}
	}
	for rest := contents; len(rest) > 0; {
		r, size := utf8.DecodeRune(rest)
		if r == 0xfffe || r == 0xffff || r == utf8.RuneError && size == 1 {
			return false
		}
		rest = rest[size:]
	}
	return true
}
