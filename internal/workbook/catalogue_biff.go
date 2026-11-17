package workbook

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"excellens/internal/formats"
)

// Lbl, SupBook and ExternSheet are captured during the existing Globals pass.
// Reading the directory never re-reads a large SST or evaluates name tokens.
type biffCatalogueCapture struct {
	names      []biffCatalogueName
	nameCount  int
	sheets     []string // original BOUNDSHEET order, including chart/macro sheets
	books      []bool   // true only for the self-referencing SupBook marker
	links      [][3]uint16
	bytes      int
	incomplete bool
	stopped    map[uint16]bool
}

type biffCatalogueName struct {
	ordinal int
	data    []byte
}

type catalogueBIFFTerm struct {
	reference string
	nameID    string
}

// Each sequence retains a prefix. Once capacity or framing is lost, later
// records cannot fill its gaps and silently change a source record ordinal.
func (c *biffCatalogueCapture) reserve(id uint16, count, size int) bool {
	if c.stopped[id] {
		return false
	}
	if count >= formats.CatalogueEntries || size > formats.CatalogueBytes-c.bytes {
		c.stop(id)
		return false
	}
	c.bytes += size
	return true
}

func (c *biffCatalogueCapture) stop(id uint16) {
	if c.stopped == nil {
		c.stopped = make(map[uint16]bool)
	}
	c.stopped[id], c.incomplete = true, true
}

func (c *biffCatalogueCapture) capture(id uint16, data []byte) {
	switch id {
	case 0x0085:
		if !c.reserve(id, len(c.sheets), 32+len(data)*2) {
			return
		}
		name := ""
		if len(data) >= 8 {
			var err error
			name, _, err = biffCatalogueString(data[7:], int(data[6]))
			if err != nil {
				c.incomplete = true
			}
		} else {
			c.incomplete = true
		}
		c.sheets = append(c.sheets, name)
	case 0x01AE:
		if !c.reserve(id, len(c.books), 2) {
			return
		}
		c.books = append(c.books, len(data) == 4 && xlsUint16(data, 2) == 0x0401)
	case 0x0017:
		if len(data) < 2 || int(xlsUint16(data, 0))*6 != len(data)-2 {
			c.stop(id)
			return
		}
		for at := 2; at < len(data); at += 6 {
			if !c.reserve(id, len(c.links), 12) {
				return
			}
			c.links = append(c.links, [3]uint16{xlsUint16(data, at), xlsUint16(data, at+2), xlsUint16(data, at+4)})
		}
	case 0x0018:
		c.nameCount++
		if !c.reserve(id, len(c.names), len(data)+64) {
			return
		}
		c.names = append(c.names, biffCatalogueName{c.nameCount, append([]byte(nil), data...)})
	}
}

var biffCatalogueBuiltinNames = [...]string{"Consolidate_Area", "Auto_Open", "Auto_Close", "Extract", "Database", "Criteria", "Print_Area", "Print_Titles", "Recorder", "Data_Form", "Auto_Activate", "Auto_Deactivate", "Sheet_Title", "_FilterDatabase"}

func (c *biffCatalogueCapture) finish() Catalogue {
	b := newCatalogueBuilder()
	b.value.NamesIncomplete = c.nameCount != len(c.names)
	labels := make(map[uint32]CatalogueItem)
	formulas := [][]byte{}
	for _, record := range c.names {
		data := record.data
		item, tokens := c.name(data, record.ordinal)
		if !b.add(item) {
			break
		}
		formulas = append(formulas, tokens)
		labels[uint32(record.ordinal)] = item
	}
	if c.incomplete {
		b.notice("部分BIFF8名称或外部表关系未能完整读取，目录不完整")
	}
	for i := range b.value.Items {
		item := &b.value.Items[i]
		if item.Status == "unresolved" {
			definition, terms, err := c.expression(formulas[i], labels, item.Scope)
			if err == nil {
				item.Definition, item.biffTerms = definition, terms
			} else {
				item.Status, item.Reason = "unavailable", err.Error()
			}
		}
	}
	bounded := newCatalogueBuilder()
	bounded.value.Complete, bounded.value.Notices = b.value.Complete, b.value.Notices
	bounded.value.NamesIncomplete = b.value.NamesIncomplete
	for _, item := range b.value.Items {
		if !bounded.add(item) {
			break
		}
	}
	return bounded.value
}

func (c *biffCatalogueCapture) name(data []byte, ordinal int) (CatalogueItem, []byte) {
	item := CatalogueItem{ID: "name-" + strconv.Itoa(ordinal), Kind: "name", Status: "unresolved"}
	if len(data) < 15 {
		item.Status, item.Reason = "invalid", "BIFF8名称记录头截断"
		item.TokenHex = hex.EncodeToString(data)
		return item, nil
	}
	flags, chars, length, scope := xlsUint16(data, 0), int(data[3]), int(xlsUint16(data, 4)), int(xlsUint16(data, 8))
	item.Hidden = flags&1 != 0
	if scope > 0 {
		item.ScopeID = strconv.Itoa(scope)
		if scope > len(c.sheets) || c.sheets[scope-1] == "" {
			item.Status, item.Reason = "invalid", "BIFF8名称作用域无效"
		} else {
			item.Scope = c.sheets[scope-1]
		}
	}
	var size int
	var err error
	item.Name, size, err = biffCatalogueString(data[14:], chars)
	if err != nil {
		item.Status, item.Reason = "invalid", "BIFF8名称文字长度或编码无效"
		item.TokenHex = hex.EncodeToString(data[14:])
		return item, nil
	}
	if flags&0x20 != 0 {
		if chars == 1 && len(item.Name) == 1 && int(item.Name[0]) < len(biffCatalogueBuiltinNames) {
			item.Name = "_xlnm." + biffCatalogueBuiltinNames[item.Name[0]]
		} else {
			found := false
			for _, name := range biffCatalogueBuiltinNames {
				if strings.EqualFold(item.Name, name) {
					item.Name, found = "_xlnm."+name, true
					break
				}
			}
			if !found {
				item.Status, item.Reason = "unavailable", "BIFF8内置名称代码或文字尚未识别"
			}
		}
	}
	start := 14 + size
	if length > len(data)-start {
		if item.Status != "invalid" {
			item.Status, item.Reason = "unavailable", "跨记录或不完整的BIFF8名称定义尚未完整解码"
		}
		item.TokenHex = hex.EncodeToString(data[start:])
		c.incomplete = true
		return item, nil
	}
	tokens := data[start : start+length]
	item.TokenHex = hex.EncodeToString(tokens)
	if item.Status != "invalid" && flags&0x0E != 0 {
		item.Status, item.Reason = "unavailable", "宏或函数名称只保留记录，不执行或定位"
	}
	return item, tokens
}

// Reject broken UTF-16 rather than manufacture replacement characters that
// could collide with a different source name. The first byte is fHighByte.
func biffCatalogueString(data []byte, chars int) (string, int, error) {
	if len(data) < 1 || chars < 1 || data[0]&^byte(1) != 0 {
		return "", 0, errors.New("BIFF8名称文字编码无效")
	}
	wide := data[0] == 1
	size := chars
	if wide {
		size *= 2
	}
	if size > len(data)-1 {
		return "", 0, errors.New("BIFF8名称文字截断")
	}
	if wide {
		for at := 1; at < size+1; at += 2 {
			u := xlsUint16(data, at)
			if u >= 0xD800 && u <= 0xDBFF {
				if at+2 >= size+1 || !utf16.IsSurrogate(rune(xlsUint16(data, at+2))) || xlsUint16(data, at+2) < 0xDC00 {
					return "", 0, errors.New("BIFF8名称UTF-16代理项无效")
				}
				at += 2
			} else if u >= 0xDC00 && u <= 0xDFFF {
				return "", 0, errors.New("BIFF8名称UTF-16代理项无效")
			}
		}
	}
	return decodeBIFFChars(data[1:1+size], wide), size + 1, nil
}

func (c *biffCatalogueCapture) sheet(link uint16) (string, error) {
	if int(link) >= len(c.links) {
		return "", errors.New("BIFF8名称的工作表引用编号无效")
	}
	x := c.links[link]
	if int(x[0]) >= len(c.books) || !c.books[x[0]] {
		return "", errors.New("外部工作簿引用不自动定位")
	}
	if x[1] != x[2] {
		return "", errors.New("三维工作表引用尚不支持定位")
	}
	if int(x[1]) >= len(c.sheets) || c.sheets[x[1]] == "" {
		return "", errors.New("BIFF8名称工作表编号无效")
	}
	return "'" + strings.ReplaceAll(c.sheets[x[1]], "'", "''") + "'!", nil
}

func (c *biffCatalogueCapture) expression(tokens []byte, labels map[uint32]CatalogueItem, scope string) (string, []catalogueBIFFTerm, error) {
	type operand struct {
		text  string
		terms []catalogueBIFFTerm
	}
	stack := []operand{}
	for at := 0; at < len(tokens); {
		kind := tokens[at]
		at++
		if kind&0x80 != 0 {
			return "", nil, errors.New("BIFF8名称表达式标记含无效的保留位，原始标记已保留")
		}
		if kind >= 0x20 {
			kind = (kind & 0x1F) | 0x20
		}
		switch kind {
		case 0x3A, 0x3B, 0x24, 0x25:
			prefix := ""
			if kind == 0x3A || kind == 0x3B {
				if at+2 > len(tokens) {
					return "", nil, errors.New("BIFF8名称引用截断")
				}
				var err error
				prefix, err = c.sheet(xlsUint16(tokens, at))
				if err != nil {
					return "", nil, err
				}
				at += 2
			} else if scope == "" {
				return "", nil, errors.New("未限定工作表的BIFF8名称不能确定固定位置")
			}
			area := kind == 0x3B || kind == 0x25
			size := 4
			if area {
				size = 8
			}
			if at+size > len(tokens) {
				return "", nil, errors.New("BIFF8名称区域坐标截断")
			}
			r1, c1 := xlsUint16(tokens, at), xlsUint16(tokens, at+2)
			r2, c2 := r1, c1
			if area {
				r2, c1, c2 = xlsUint16(tokens, at+2), xlsUint16(tokens, at+4), xlsUint16(tokens, at+6)
			}
			if c1&0xC000 != 0 || c2&0xC000 != 0 {
				return "", nil, errors.New("BIFF8相对名称需要活动单元格上下文")
			}
			if c1 > 255 || c2 > 255 || r2 < r1 || c2 < c1 {
				return "", nil, errors.New("BIFF8名称区域坐标无效")
			}
			value := prefix + "$" + ColumnName(int(c1)+1) + "$" + strconv.Itoa(int(r1)+1)
			if area {
				value += ":$" + ColumnName(int(c2)+1) + "$" + strconv.Itoa(int(r2)+1)
			}
			stack = append(stack, operand{value, []catalogueBIFFTerm{{reference: value}}})
			at += size
		case 0x23:
			if at+4 > len(tokens) {
				return "", nil, errors.New("BIFF8名称别名编号截断")
			}
			index := xlsUint32(tokens, at)
			at += 4
			label, ok := labels[index]
			if index < 1 || !ok || label.Name == "" || label.Status == "invalid" {
				return "", nil, errors.New("BIFF8名称别名编号无效或原始记录未读取")
			}
			name := label.Name
			if label.Scope != "" {
				name = "'" + strings.ReplaceAll(label.Scope, "'", "''") + "'!" + name
			}
			stack = append(stack, operand{name, []catalogueBIFFTerm{{nameID: label.ID}}})
		case 0x10:
			if len(stack) < 2 {
				return "", nil, errors.New("BIFF8名称联合区域操作数不足")
			}
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			previous := &stack[len(stack)-1]
			if len(previous.text)+len(last.text)+1 > formats.CatalogueDefinitionMax || len(previous.terms)+len(last.terms) > formats.CatalogueAreas {
				return "", nil, errors.New("BIFF8名称定义或区域数量超过容量")
			}
			previous.text += "," + last.text
			previous.terms = append(previous.terms, last.terms...)
		case 0x15:
			if len(stack) < 1 {
				return "", nil, errors.New("BIFF8名称括号操作数不足")
			}
		default:
			return "", nil, fmt.Errorf("BIFF8名称含尚未解码的表达式标记0x%02X，原始标记已保留", kind)
		}
		if len(stack) > formats.CatalogueAreas {
			return "", nil, errors.New("BIFF8名称区域数量超过容量")
		}
	}
	if len(stack) != 1 {
		return "", nil, errors.New("BIFF8名称没有唯一的可还原定义")
	}
	return stack[0].text, stack[0].terms, nil
}
