package workbook

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// Scan tags without decoding cell values or retaining sheetData. One explicit
// read verifies all source XML bytes once, while buffering only a bounded rule
// block and the root namespace declaration. No used-row guess finds the tail.
func scanDataRuleXML(ctx context.Context, input io.Reader, total int64, b *dataRuleBuilder, progress func(int64, int64, string)) error {
	counted := &countingReader{r: catalogueContextReader{ctx, input}}
	r := bufio.NewReaderSize(counted, 1<<20)
	var names [formats.DataRuleXMLDepth][]byte
	var buffer, root, block []byte
	depth, blocks, dataCount := 0, 0, 0
	extensions, captureDepth, extensionLists := 0, 0, 0
	var extList, ext []byte
	var parents [][]byte
	closed, dataClosed, capturing := false, false, false
	inData := false
	last := int64(0)
	appendBlock := func(part []byte) error {
		if len(part) > formats.DataRuleBlockBytes-len(block) {
			return errors.New("数据规则XML块超过读取容量，未发布部分规则")
		}
		block = append(block, part...)
		return nil
	}
	if progress != nil {
		progress(0, total, "正在读取源数据规则")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, readErr := r.ReadSlice('<')
		text := chunk
		hasTag := len(chunk) > 0 && chunk[len(chunk)-1] == '<'
		if hasTag {
			text = chunk[:len(chunk)-1]
		}
		if counted.n-int64(r.Buffered())-int64(len(chunk)) == 0 {
			text = bytes.TrimPrefix(text, []byte{0xEF, 0xBB, 0xBF})
		}
		if depth == 0 && len(bytes.TrimSpace(text)) > 0 {
			return errors.New("数据规则所在工作表根外含无效文字")
		}
		if capturing {
			if err := appendBlock(text); err != nil {
				return err
			}
		}
		if hasTag {
			tag, err := readXMLTag(r, buffer)
			buffer = tag
			if err != nil {
				return err
			}
			info := inspectXMLTag(tag)
			if capturing {
				if err := appendBlock(tag); err != nil {
					return err
				}
			}
			if !info.valid {
				if !bytes.HasPrefix(tag, []byte("<!--")) && !bytes.HasPrefix(tag, []byte("<![CDATA[")) && !bytes.HasPrefix(tag, []byte("<?")) {
					return errors.New("数据规则所在工作表含未知XML声明")
				}
				if bytes.HasPrefix(tag, []byte("<![CDATA[")) && depth == 0 || bytes.HasPrefix(tag, []byte("<?xml")) && len(root) != 0 {
					return errors.New("数据规则所在工作表XML声明位置无效")
				}
			} else {
				// Reuse the per-depth name buffers, not a string allocation for
				// every cell tag in a very large worksheet.
				at := 1
				if info.closing {
					at++
				}
				end := at
				for end < len(tag) && !isXMLSpace(tag[end]) && tag[end] != '/' && tag[end] != '>' {
					end++
				}
				name := tag[at:end]
				if len(name) == 0 || len(name) > formats.DataRuleQNameBytes || depth > formats.DataRuleXMLDepth || closed {
					return errors.New("数据规则所在工作表XML根或层级无效")
				}
				if info.closing {
					if depth == 0 || !bytes.Equal(names[depth-1], name) {
						return errors.New("数据规则所在工作表XML结束标签不匹配")
					}
					tail := end
					for tail < len(tag) && isXMLSpace(tag[tail]) {
						tail++
					}
					if tail != len(tag)-1 || tag[tail] != '>' {
						return errors.New("数据规则所在工作表XML结束标签含无效属性或内容")
					}
					if depth == 2 && string(info.name) == "sheetData" {
						dataClosed = true
						inData = false
					}
					if depth == captureDepth && capturing {
						if err := readDataRuleBlockWithParents(ctx, parents, block, b); err != nil {
							return err
						}
						capturing, block = false, nil
						parents = nil
						captureDepth = 0
					}
					if depth == 3 && string(info.name) == "ext" {
						ext = nil
					}
					if depth == 2 && string(info.name) == "extLst" {
						extList = nil
						ext = nil
					}
					depth--
					closed = depth == 0
				} else {
					if depth == formats.DataRuleXMLDepth {
						return errors.New("数据规则所在工作表XML层级超过读取容量")
					}
					if depth == 0 {
						if len(root) != 0 || string(info.name) != "worksheet" || info.selfClosing || len(tag) > formats.DataRuleTextBytes {
							return errors.New("数据规则工作表根无效或超过读取容量")
						}
						root = bytes.Clone(tag)
						if _, err := dataRuleRootChild(root, nil); err != nil {
							return err
						}
					}
					if depth == 1 && (string(info.name) == "dataValidations" || string(info.name) == "sheetData") {
						matched, err := dataRuleRootChild(root, tag)
						if err != nil {
							return err
						}
						if matched && string(info.name) == "sheetData" {
							dataCount++
							dataClosed = info.selfClosing
							inData = !info.selfClosing
							if dataCount != 1 {
								return errors.New("数据规则所在工作表数据容器重复")
							}
						} else if matched {
							blocks++
							if blocks != 1 || dataCount != 1 || !dataClosed {
								return errors.New("数据规则容器重复或未位于源数据容器之后")
							}
							if err := appendBlock(tag); err != nil {
								return err
							}
							if info.selfClosing {
								if err := readDataRuleBlock(ctx, root, block, b); err != nil {
									return err
								}
								block = nil
							} else {
								capturing = true
								captureDepth = 2
								parents = [][]byte{root}
							}
						} else if string(info.name) == "dataValidations" {
							b.notice("工作表含其他命名空间的数据规则，未按普通规则解释")
						}
					}
					if depth == 1 && string(info.name) == "extLst" {
						if len(tag) > formats.DataRuleTextBytes {
							return errors.New("数据规则扩展根声明超过读取容量")
						}
						matched, err := dataRuleRootChild(root, tag)
						if err != nil {
							return err
						}
						if matched {
							extensionLists++
							if extensionLists > 1 {
								return errors.New("工作表扩展容器重复，不能确认唯一的数据规则来源")
							}
						}
						ext = nil
						extList = nil
						if !info.selfClosing {
							extList = bytes.Clone(tag)
						}
					}
					if depth == 2 && extList != nil && string(info.name) == "ext" {
						if len(tag) > formats.DataRuleTextBytes {
							return errors.New("数据规则扩展声明超过读取容量")
						}
						ext = nil
						if !info.selfClosing {
							ext = bytes.Clone(tag)
						}
					}
					if string(info.name) == "dataValidations" && depth > 1 && extList != nil {
						matched := false
						if depth == 3 && ext != nil {
							var err error
							matched, err = dataRuleExtensionHeader(root, extList, ext, tag)
							if err != nil {
								return err
							}
						}
						if matched {
							extensions++
							if extensions > 1 || dataCount != 1 || !dataClosed {
								return errors.New("数据规则扩展声明重复或未位于真实源数据之后")
							}
							if err := appendBlock(tag); err != nil {
								return err
							}
							if info.selfClosing {
								if err := readDataRuleBlockWithParents(ctx, [][]byte{root, extList, ext}, block, b); err != nil {
									return err
								}
								block = nil
							} else {
								capturing = true
								captureDepth = 4
								parents = [][]byte{root, extList, ext}
							}
						} else {
							b.notice("工作表含未知命名空间、位置或声明的数据规则扩展，未完整读取")
						}
					} else if string(info.name) == "dataValidations" && depth > 1 && !inData {
						b.notice("数据规则出现在尚未解释的工作表结构中，未完整读取")
					}
					if !info.selfClosing {
						names[depth] = append(names[depth][:0], name...)
						depth++
					}
				}
			}
		}
		current := counted.n - int64(r.Buffered())
		if progress != nil && current-last >= 8<<20 {
			progress(current, total, "正在读取源数据规则")
			last = current
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			return readErr
		}
	}
	if !closed || depth != 0 || capturing || dataCount != 1 || !dataClosed {
		return errors.New("数据规则所在工作表XML未完整结束")
	}
	return ctx.Err()
}

func dataRuleRootChild(root, child []byte) (bool, error) {
	d := xml.NewDecoder(io.MultiReader(bytes.NewReader(root), bytes.NewReader(child)))
	token, err := d.Token()
	if err != nil {
		return false, err
	}
	if err := dataRuleXMLToken(token, 0); err != nil {
		return false, err
	}
	node, ok := token.(xml.StartElement)
	if !ok || node.Name.Local != "worksheet" || !catalogueNamespace(node.Name.Space) {
		return false, errors.New("数据规则工作表根命名空间无效")
	}
	if child == nil {
		return true, nil
	}
	token, err = d.Token()
	if err != nil {
		return false, err
	}
	if err := dataRuleXMLToken(token, 1); err != nil {
		return false, err
	}
	next, ok := token.(xml.StartElement)
	return ok && next.Name.Space == node.Name.Space, nil
}

func dataRuleXMLToken(token xml.Token, depth int) error {
	if node, ok := token.(xml.StartElement); ok {
		attributes := formats.DataRuleAttributeCount
		if depth == 0 {
			attributes = formats.DataRuleRootAttributes
		}
		if depth >= formats.DataRuleXMLDepth || len(node.Attr) > attributes {
			return errors.New("数据规则XML层级或属性数量超过读取容量")
		}
		seen := make(map[xml.Name]bool, len(node.Attr))
		for _, attr := range node.Attr {
			if seen[attr.Name] || len(attr.Value) > formats.DataRuleTextBytes {
				return errors.New("数据规则XML属性重复或文字超过读取容量")
			}
			seen[attr.Name] = true
		}
	}
	return nil
}

func readDataRuleBlock(ctx context.Context, root, block []byte, b *dataRuleBuilder) error {
	return readDataRuleBlockWithParents(ctx, [][]byte{root}, block, b)
}

func readDataRuleBlockWithParents(ctx context.Context, parents [][]byte, block []byte, b *dataRuleBuilder) error {
	if len(parents) != 1 && len(parents) != 3 {
		return errors.New("数据规则来源层级无效")
	}
	var prefix, suffix strings.Builder
	for _, parent := range parents {
		prefix.Write(parent)
	}
	for at := len(parents) - 1; at >= 0; at-- {
		suffix.WriteString("</" + catalogueTagQName(parents[at]) + ">")
	}
	d := xml.NewDecoder(io.MultiReader(strings.NewReader(prefix.String()), bytes.NewReader(block), strings.NewReader(suffix.String())))
	depth, expected, count := 0, -1, 0
	var rootNamespace, namespace string
	containerDepth := len(parents) + 1
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		startOffset := d.InputOffset()
		token, err := d.Token()
		if err == io.EOF {
			if depth != 0 || expected >= 0 && expected != count {
				return errors.New("数据规则XML未完整结束或声明数量不一致")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if err := dataRuleXMLToken(token, depth); err != nil {
			return err
		}
		switch node := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				rootNamespace = node.Name.Space
				continue
			}
			if depth <= len(parents) {
				continue
			}
			if depth == containerDepth {
				namespace = node.Name.Space
				if node.Name.Local != "dataValidations" || len(parents) == 1 && namespace != rootNamespace || len(parents) == 3 && namespace != x14DataRulesNamespace {
					return errors.New("数据规则缓存块不是源工作表的真实规则容器")
				}
				container := DataRuleContainer{Namespace: namespace, Attributes: make(map[string]string)}
				for _, attr := range node.Attr {
					if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
						continue
					}
					container.Attributes[dataRuleAttributeKey(attr.Name.Space, attr.Name.Local)] = attr.Value
					if attr.Name.Local == "count" && attr.Name.Space == "" {
						expected, err = strconv.Atoi(attr.Value)
						if err != nil || expected < 0 || expected > formats.DataRuleCount {
							return errors.New("数据规则声明数量无效或超过读取容量")
						}
					} else if attr.Name.Local == "disablePrompts" && attr.Name.Space == "" {
						value, ok := sourceBool([]byte(attr.Value))
						if !ok {
							return errors.New("数据规则全局提示开关无效")
						}
						container.DisablePrompts = value
					} else if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" && (attr.Name.Space != "" || attr.Name.Local != "xWindow" && attr.Name.Local != "yWindow") {
						b.notice("数据规则容器含尚未解释的属性")
					}
				}
				if err := b.addContainer(container); err != nil {
					return err
				}
				continue
			}
			if depth != containerDepth+1 || node.Name.Local != "dataValidation" || node.Name.Space != namespace {
				b.notice("数据规则容器含扩展或未知定义，原文保留在源文件")
				if err := skipDataRuleElement(ctx, d, depth); err != nil {
					return err
				}
				depth--
				continue
			}
			rule, err := readSourceDataRule(ctx, d, node, b.index.maxRow, b.index.maxCol)
			if err != nil {
				return err
			}
			first, last := int(startOffset)-prefix.Len(), int(d.InputOffset())-prefix.Len()
			if first < 0 || last > len(block) || last < first || last-first > formats.DataRuleTextBytes {
				return errors.New("单项数据规则原始定义超过读取容量，未截断为有效规则")
			}
			rule.RawXML = string(block[first:last])
			if err := b.add(rule); err != nil {
				return err
			}
			count++
			depth--
		case xml.EndElement:
			depth--
		case xml.CharData:
			if len(bytes.TrimSpace(node)) > 0 {
				return errors.New("数据规则容器含无效文字")
			}
		}
	}
}

func skipDataRuleElement(ctx context.Context, d *xml.Decoder, depth int) error {
	inner := 1
	for inner > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if err := dataRuleXMLToken(token, depth+inner-1); err != nil {
			return err
		}
		switch token.(type) {
		case xml.StartElement:
			inner++
		case xml.EndElement:
			inner--
		}
	}
	return nil
}
