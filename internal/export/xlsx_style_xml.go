package export

// Preserve original XML bytes, including unknown children and namespace/QName
// values. Only owned start-tag attributes and direct main-namespace children
// are patched. No lossy VisualStyle -> complete source stylesheet conversion.
import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

type styleXMLSpan struct{ start, headEnd, close, end int }
type styleXMLSection struct {
	name         string
	span         styleXMLSpan
	original     []styleXMLSpan
	added        []string
	intern       map[string]int
	headerGrowth int
}
type styleXMLDoc struct {
	raw         string
	ns          string
	root        styleXMLSpan
	sections    map[string]*styleXMLSection
	top         map[string]styleXMLSpan
	work        int
	outputBytes int
}
type styleXMLEdit struct {
	start, end int
	parts      []string
}
type styleXMLChild struct {
	span styleXMLSpan
	name xml.Name
}
type styleXMLFragment struct {
	ctx      context.Context
	raw      string
	span     styleXMLSpan
	name     xml.Name
	attrs    []xml.Attr
	scope    map[string]string
	children []styleXMLChild
}
type styleXMLWrapper struct {
	head, tail string
	depth      int
}

var styleSectionOrder = []string{"numFmts", "fonts", "fills", "borders", "cellStyleXfs", "cellXfs", "cellStyles", "dxfs", "tableStyles", "colors", "extLst"}
var styleRecordNames = map[string]string{"numFmts": "numFmt", "fonts": "font", "fills": "fill", "borders": "border", "cellStyleXfs": "xf", "cellXfs": "xf"}

func styleSpace(b byte) bool { return b == ' ' || b == '\n' || b == '\r' || b == '\t' }
func styleQName(raw string) string {
	i := 1
	if i < len(raw) && raw[i] == '/' {
		i++
	}
	start := i
	for i < len(raw) && !styleSpace(raw[i]) && raw[i] != '>' && raw[i] != '/' {
		i++
	}
	return raw[start:i]
}
func styleQualified(raw, name string) string {
	q := styleQName(raw)
	if at := strings.IndexByte(q, ':'); at >= 0 {
		return q[:at+1] + name
	}
	return name
}
func styleClose(raw string) string { return "</" + styleQName(raw) + ">" }
func styleOpen(raw string) string {
	if strings.HasSuffix(raw, "/>") {
		return raw[:len(raw)-2] + ">"
	}
	return raw
}
func styleNamespaces(parent map[string]string, attrs []xml.Attr) map[string]string {
	var out map[string]string
	for _, a := range attrs {
		key := ""
		if a.Name.Space == "xmlns" {
			key = "xmlns:" + a.Name.Local
		} else if a.Name.Space == "" && a.Name.Local == "xmlns" {
			key = "xmlns"
		} else {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(parent)+1)
			for k, v := range parent {
				out[k] = v
			}
		}
		out[key] = a.Value
	}
	if out == nil {
		return parent
	}
	return out
}
func styleAttr(attrs []xml.Attr, name string) (string, bool, error) {
	value := ""
	found := false
	for _, a := range attrs {
		if a.Name.Space == "" && a.Name.Local == name {
			if found {
				return "", false, fmt.Errorf("样式属性重复：%s", name)
			}
			value, found = a.Value, true
		}
	}
	return value, found, nil
}
func styleIntAttr(attrs []xml.Attr, name string, fallback int) (int, error) {
	raw, found, err := styleAttr(attrs, name)
	if err != nil {
		return 0, err
	}
	if !found {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 || v >= formats.XLSXStyleRecords {
		return 0, fmt.Errorf("样式编号无效：%s", name)
	}
	return v, nil
}

func validateStyleAttributes(attrs []xml.Attr) error {
	if len(attrs) < 2 {
		return nil
	}
	seen := make(map[xml.Name]struct{}, len(attrs))
	for _, attr := range attrs {
		if _, duplicate := seen[attr.Name]; duplicate {
			return errors.New("样式XML属性或命名空间重复，无法可靠解释")
		}
		seen[attr.Name] = struct{}{}
	}
	return nil
}

func parseStyleXML(ctx context.Context, raw string) (*styleXMLDoc, error) {
	if len(raw) > formats.XLSXStylePartBytes {
		return nil, errors.New("XLSX样式部件超过64MiB")
	}
	doc := &styleXMLDoc{raw: raw, sections: make(map[string]*styleXMLSection), top: make(map[string]styleXMLSpan), outputBytes: len(raw)}
	decoder := xml.NewDecoder(styleContextReader{ctx, strings.NewReader(raw)})
	type frame struct {
		span    styleXMLSpan
		name    xml.Name
		section string
		record  bool
	}
	var stack []frame
	rootSeen, rootDone := false, false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before := int(decoder.InputOffset())
		token, err := decoder.Token()
		after := int(decoder.InputOffset())
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("样式XML无效：%w", err)
		}
		switch t := token.(type) {
		case xml.Directive:
			return nil, errors.New("样式XML含不支持的声明")
		case xml.StartElement:
			if len(stack) >= formats.XLSXStyleXMLDepth || after-before > formats.XLSXStyleHeaderBytes {
				return nil, errors.New("样式XML层级或标签超过读取容量")
			}
			if err := validateStyleAttributes(t.Attr); err != nil {
				return nil, err
			}
			f := frame{span: styleXMLSpan{start: before, headEnd: after}, name: t.Name}
			if len(stack) == 0 {
				if rootSeen || rootDone || t.Name.Local != "styleSheet" {
					return nil, errors.New("样式XML根元素无效")
				}
				rootSeen = true
				doc.ns = t.Name.Space
				q := styleQName(raw[before:after])
				scope := styleNamespaces(nil, t.Attr)
				prefix := "xmlns"
				if at := strings.IndexByte(q, ':'); at >= 0 {
					prefix = "xmlns:" + q[:at]
				}
				if scope[prefix] != t.Name.Space {
					return nil, errors.New("样式根命名空间未可靠声明")
				}
			} else if len(stack) == 1 && t.Name.Space == doc.ns && knownStyleSection(t.Name.Local) {
				if _, ok := doc.top[t.Name.Local]; ok {
					return nil, errors.New("样式目录重复，无法完整生成快照")
				}
				f.section = t.Name.Local
				if _, managed := styleRecordNames[t.Name.Local]; managed {
					doc.sections[t.Name.Local] = &styleXMLSection{name: t.Name.Local, intern: make(map[string]int)}
				}
			} else if len(stack) == 2 && styleRecordNames[stack[1].section] != "" {
				f.section = stack[1].section
				if t.Name.Local == styleRecordNames[f.section] {
					if t.Name.Space != doc.ns {
						return nil, errors.New("样式目录记录命名空间无法可靠解释")
					}
					f.record = true
				}
			}
			stack = append(stack, f)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("样式XML结束标签未匹配")
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			f.span.close, f.span.end = before, after
			if before == after {
				f.span.close = f.span.headEnd
			} // empty-element End token
			if f.record {
				s := doc.sections[f.section]
				if len(s.original) >= formats.XLSXStyleRecords {
					return nil, errors.New("源样式目录记录数量超限")
				}
				s.original = append(s.original, f.span)
				doc.work += 96
				if doc.work > formats.XLSXStyleWorkBytes {
					return nil, errors.New("源样式索引超过快照工作容量")
				}
			} else if len(stack) == 1 && f.name.Space == doc.ns && f.section != "" {
				doc.top[f.name.Local] = f.span
				if s := doc.sections[f.section]; s != nil {
					s.span = f.span
				}
			} else if len(stack) == 0 {
				doc.root = f.span
				rootDone = true
			}
		case xml.CharData:
			if len(stack) == 0 && strings.TrimSpace(string(t)) != "" {
				return nil, errors.New("样式XML根元素外含内容")
			}
		}
	}
	if !rootDone || len(stack) != 0 {
		return nil, errors.New("样式XML没有完整结束")
	}
	return doc, nil
}

func (d *styleXMLDoc) reserve(n int) error {
	if n < 0 || n > formats.XLSXStyleWorkBytes-d.work {
		return errors.New("条件样式去重及快照超过工作容量，请缩小范围或关闭原始格式")
	}
	d.work += n
	return nil
}
func (d *styleXMLDoc) section(name string) *styleXMLSection {
	s := d.sections[name]
	if s == nil {
		s = &styleXMLSection{name: name, intern: make(map[string]int)}
		d.sections[name] = s
	}
	return s
}
func (s *styleXMLSection) count() int { return len(s.original) + len(s.added) }
func (d *styleXMLDoc) record(section string, id int) (string, error) {
	s := d.section(section)
	if id < 0 || id >= s.count() {
		return "", fmt.Errorf("源%s样式编号不存在：%d", section, id)
	}
	if id < len(s.original) {
		p := s.original[id]
		return d.raw[p.start:p.end], nil
	}
	return s.added[id-len(s.original)], nil
}
func (d *styleXMLDoc) appendRecord(section, raw string) (int, error) {
	s := d.section(section)
	if id, ok := s.intern[raw]; ok {
		return id, nil
	}
	if s.count() >= formats.XLSXStyleRecords {
		return 0, errors.New("XLSX样式数量达到格式上限，请缩小范围或关闭原始格式")
	}
	count := s.count() + 1
	q := styleQualified(d.raw[d.root.start:d.root.headEnd], section)
	growth := len("<"+q+` count="`+strconv.Itoa(count)+`">`) + len("</"+q+">")
	if s.span.headEnd > 0 {
		head, err := patchStyleHead(d.raw[s.span.start:s.span.headEnd], map[string]string{"count": strconv.Itoa(count)}, true)
		if err != nil {
			return 0, err
		}
		growth = len(head) - (s.span.headEnd - s.span.start)
		if s.span.close == s.span.end {
			growth += len(styleClose(head))
		}
	}
	outputBytes := d.outputBytes + len(raw) + growth - s.headerGrowth
	if outputBytes > formats.XLSXStylePartBytes {
		return 0, errors.New("派生样式XML超过部件容量，请缩小范围或关闭原始格式")
	}
	if err := d.reserve(len(raw)*2 + 192); err != nil {
		return 0, err
	}
	d.outputBytes = outputBytes
	s.headerGrowth = growth
	id := s.count()
	s.added = append(s.added, raw)
	s.intern[raw] = id
	return id, nil
}
func (d *styleXMLDoc) wrapper(section string) styleXMLWrapper {
	root := styleOpen(d.raw[d.root.start:d.root.headEnd])
	s := d.section(section)
	head := "<" + styleQualified(root, section) + ">"
	if s.span.headEnd > 0 {
		head = styleOpen(d.raw[s.span.start:s.span.headEnd])
	}
	return styleXMLWrapper{head: root + head, tail: styleClose(head) + styleClose(root), depth: 2}
}
func (w styleXMLWrapper) within(head string) styleXMLWrapper {
	return styleXMLWrapper{head: w.head + styleOpen(head), tail: styleClose(head) + w.tail, depth: w.depth + 1}
}

func parseStyleFragment(ctx context.Context, wrapper styleXMLWrapper, raw string) (*styleXMLFragment, error) {
	d := xml.NewDecoder(styleContextReader{ctx, io.MultiReader(strings.NewReader(wrapper.head), strings.NewReader(raw), strings.NewReader(wrapper.tail))})
	fragment := &styleXMLFragment{ctx: ctx, raw: raw}
	type frame struct {
		start, headEnd int
		name           xml.Name
		scope          map[string]string
	}
	var stack []frame
	base := len(wrapper.head)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before := int(d.InputOffset())
		token, err := d.Token()
		after := int(d.InputOffset())
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.Directive:
			return nil, errors.New("样式片段含不支持的声明")
		case xml.StartElement:
			if len(stack) >= formats.XLSXStyleXMLDepth || after-before > formats.XLSXStyleHeaderBytes {
				return nil, errors.New("样式片段层级或标签超过读取容量")
			}
			if err := validateStyleAttributes(t.Attr); err != nil {
				return nil, err
			}
			var parent map[string]string
			if len(stack) > 0 {
				parent = stack[len(stack)-1].scope
			}
			scope := parent
			// Only the fragment root's scope is needed when transplanting it.
			// Descendants retain original bytes and XML decoder namespace checks;
			// do not clone large namespace maps for every unknown child.
			if len(stack) <= wrapper.depth {
				scope = styleNamespaces(parent, t.Attr)
			}
			f := frame{start: before - base, headEnd: after - base, name: t.Name, scope: scope}
			stack = append(stack, f)
			if len(stack) == wrapper.depth+1 {
				fragment.span = styleXMLSpan{start: f.start, headEnd: f.headEnd}
				fragment.name = t.Name
				fragment.attrs = t.Attr
				fragment.scope = f.scope
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, errors.New("样式片段结束标签未匹配")
			}
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) == wrapper.depth {
				fragment.span.close, fragment.span.end = before-base, after-base
				if before == after {
					fragment.span.close = fragment.span.headEnd
				}
			}
			if len(stack) == wrapper.depth+1 {
				if len(fragment.children) >= formats.XLSXStyleChildRecords {
					return nil, errors.New("样式片段直接子项超过读取容量")
				}
				span := styleXMLSpan{start: f.start, headEnd: f.headEnd, close: before - base, end: after - base}
				if before == after {
					span.close = span.headEnd
				}
				fragment.children = append(fragment.children, styleXMLChild{span: span, name: f.name})
			}
		}
	}
	if fragment.span.start != 0 || fragment.span.end != len(raw) {
		return nil, errors.New("样式片段边界无效")
	}
	return fragment, nil
}

// Patch start attributes lexically after XML validation. This keeps namespace
// declarations and QName-valued attributes exactly in their original scope.
func patchStyleHead(raw string, values map[string]string, open bool) (string, error) {
	if len(raw) > formats.XLSXStyleHeaderBytes {
		return "", errors.New("样式标签超过写出容量")
	}
	q := styleQName(raw)
	if q == "" {
		return "", errors.New("样式标签名无效")
	}
	i := 1 + len(q)
	last := i
	seen := make(map[string]bool)
	var out strings.Builder
	out.WriteString(raw[:i])
	for i < len(raw) {
		for i < len(raw) && styleSpace(raw[i]) {
			i++
		}
		if i >= len(raw) || raw[i] == '>' || raw[i] == '/' {
			break
		}
		start := i
		for i < len(raw) && !styleSpace(raw[i]) && raw[i] != '=' && raw[i] != '>' && raw[i] != '/' {
			i++
		}
		name := raw[start:i]
		if name == "" || seen[name] {
			return "", errors.New("样式标签属性无效或重复")
		}
		seen[name] = true
		for i < len(raw) && styleSpace(raw[i]) {
			i++
		}
		if i >= len(raw) || raw[i] != '=' {
			return "", errors.New("样式标签属性缺少等号")
		}
		i++
		for i < len(raw) && styleSpace(raw[i]) {
			i++
		}
		if i >= len(raw) || (raw[i] != '\'' && raw[i] != '"') {
			return "", errors.New("样式标签属性缺少引号")
		}
		quote := raw[i]
		i++
		begin := i
		for i < len(raw) && raw[i] != quote {
			i++
		}
		if i >= len(raw) {
			return "", errors.New("样式标签属性未结束")
		}
		if value, ok := values[name]; ok {
			out.WriteString(raw[last:begin])
			out.WriteString(xmlAttr(value))
			last = i
		}
		i++
	}
	out.WriteString(raw[last:i])
	keys := make([]string, 0, len(values))
	for key := range values {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		out.WriteByte(' ')
		out.WriteString(key)
		out.WriteString(`="`)
		out.WriteString(xmlAttr(values[key]))
		out.WriteByte('"')
	}
	tail := raw[i:]
	if open {
		tail = styleOpen(tail)
	}
	out.WriteString(tail)
	if out.Len() > formats.XLSXStyleHeaderBytes {
		return "", errors.New("派生样式标签超过写出容量")
	}
	return out.String(), nil
}

func rewriteStyleFragment(fragment *styleXMLFragment, attrs map[string]string, children map[string]string, order []string) (string, error) {
	head, err := patchStyleHead(fragment.raw[:fragment.span.headEnd], attrs, len(children) > 0)
	if err != nil {
		return "", err
	}
	var edits []styleXMLEdit
	edits = append(edits, styleXMLEdit{0, fragment.span.headEnd, []string{head}})
	rank := func(name string) int {
		for i, v := range order {
			if v == name {
				return i
			}
		}
		return len(order)
	}
	seen := make(map[string]bool)
	for _, child := range fragment.children {
		if child.name.Space != fragment.name.Space {
			continue
		}
		value, ok := children[child.name.Local]
		if !ok {
			continue
		}
		if seen[child.name.Local] {
			return "", errors.New("源样式属性子项重复，不能可靠合成")
		}
		seen[child.name.Local] = true
		edits = append(edits, styleXMLEdit{child.span.start, child.span.end, []string{value}})
	}
	names := make([]string, 0, len(children))
	for name := range children {
		if !seen[name] && children[name] != "" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := rank(names[i]), rank(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	self := fragment.span.close == fragment.span.end
	for _, name := range names {
		at := fragment.span.close
		for _, child := range fragment.children {
			if child.name.Space == fragment.name.Space && rank(child.name.Local) > rank(name) {
				at = child.span.start
				break
			}
		}
		edits = append(edits, styleXMLEdit{at, at, []string{children[name]}})
	}
	if self && len(children) > 0 {
		edits = append(edits, styleXMLEdit{fragment.span.end, fragment.span.end, []string{styleClose(fragment.raw)}})
	}
	var out strings.Builder
	if err := writeStyleEdits(fragment.ctx, &out, fragment.raw, edits, formats.XLSXStylePartBytes); err != nil {
		return "", err
	}
	return out.String(), nil
}

func writeStyleEdits(ctx context.Context, dst io.Writer, raw string, edits []styleXMLEdit, limit int) error {
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start < edits[j].start
		}
		// Insertions at a child's start precede replacement of that child.
		// Stable ordering preserves schema order for simultaneous insertions.
		return edits[i].end < edits[j].end
	})
	size := len(raw)
	previous := 0
	for _, e := range edits {
		if e.start < previous || e.end < e.start || e.end > len(raw) {
			return errors.New("样式XML补丁范围冲突")
		}
		size -= e.end - e.start
		for _, part := range e.parts {
			size += len(part)
		}
		previous = e.end
	}
	if size < 0 || size > limit {
		return errors.New("派生样式XML超过部件容量")
	}
	out := styleContextWriter{ctx, dst}
	previous = 0
	for _, e := range edits {
		if _, err := io.WriteString(out, raw[previous:e.start]); err != nil {
			return err
		}
		for _, part := range e.parts {
			if _, err := io.WriteString(out, part); err != nil {
				return err
			}
		}
		previous = e.end
	}
	_, err := io.WriteString(out, raw[previous:])
	return err
}

func knownStyleSection(name string) bool {
	for _, v := range styleSectionOrder {
		if v == name {
			return true
		}
	}
	return false
}

func (d *styleXMLDoc) write(ctx context.Context, out io.Writer) error {
	var edits []styleXMLEdit
	for _, name := range styleSectionOrder {
		s := d.sections[name]
		if s == nil || len(s.added) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count := map[string]string{"count": strconv.Itoa(s.count())}
		if s.span.headEnd > 0 {
			head, err := patchStyleHead(d.raw[s.span.start:s.span.headEnd], count, true)
			if err != nil {
				return err
			}
			if s.span.close == s.span.end {
				parts := []string{head}
				parts = append(parts, s.added...)
				parts = append(parts, styleClose(head))
				edits = append(edits, styleXMLEdit{s.span.start, s.span.end, parts})
			} else {
				edits = append(edits, styleXMLEdit{s.span.start, s.span.headEnd, []string{head}}, styleXMLEdit{s.span.close, s.span.close, s.added})
			}
		} else {
			at := d.root.close
			rank := 0
			for i, n := range styleSectionOrder {
				if n == name {
					rank = i
					break
				}
			}
			for _, n := range styleSectionOrder[rank+1:] {
				if span, ok := d.top[n]; ok && span.start < at {
					at = span.start
				}
			}
			q := styleQualified(d.raw[d.root.start:d.root.headEnd], name)
			parts := []string{"<" + q + ` count="` + strconv.Itoa(s.count()) + `">`}
			parts = append(parts, s.added...)
			parts = append(parts, "</"+q+">")
			edits = append(edits, styleXMLEdit{at, at, parts})
		}
	}
	if d.root.close == d.root.end {
		head := styleOpen(d.raw[d.root.start:d.root.headEnd])
		edits = append(edits, styleXMLEdit{d.root.start, d.root.headEnd, []string{head}}, styleXMLEdit{d.root.end, d.root.end, []string{styleClose(head)}})
	}
	return writeStyleEdits(ctx, out, d.raw, edits, formats.XLSXStylePartBytes)
}
