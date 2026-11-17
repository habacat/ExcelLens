package workbook

// xmlCellAttributes reads the three hot-path attributes in one pass. The
// generic xmlAttribute helper remains the reference for row/formula tags.
// This function operates on the exact <c ...> start tag, whose attribute
// values have already been bounded by fastTagEnd, and returns borrowed slices.
func xmlCellAttributes(tag []byte) (ref, typ, style []byte) {
	i := 1
	for i < len(tag) && !isXMLSpace(tag[i]) && tag[i] != '>' && tag[i] != '/' {
		i++
	}
	for i < len(tag) {
		for i < len(tag) && (isXMLSpace(tag[i]) || tag[i] == '/') {
			i++
		}
		if i >= len(tag) || tag[i] == '>' || tag[i] == '?' {
			break
		}
		start := i
		for i < len(tag) && !isXMLSpace(tag[i]) && tag[i] != '=' && tag[i] != '/' && tag[i] != '>' {
			i++
		}
		name := tag[start:i]
		for i < len(tag) && isXMLSpace(tag[i]) {
			i++
		}
		if i >= len(tag) || tag[i] != '=' {
			continue
		}
		i++
		for i < len(tag) && isXMLSpace(tag[i]) {
			i++
		}
		if i >= len(tag) {
			break
		}
		quote := tag[i]
		if quote != '\'' && quote != '"' {
			continue
		}
		i++
		valueStart := i
		for i < len(tag) && tag[i] != quote {
			i++
		}
		value := tag[valueStart:i]
		if i < len(tag) {
			i++
		}
		if len(name) == 1 {
			switch name[0] {
			case 'r':
				if ref == nil {
					ref = value
				}
			case 't':
				if typ == nil {
					typ = value
				}
			case 's':
				if style == nil {
					style = value
				}
			}
		}
	}
	return
}
