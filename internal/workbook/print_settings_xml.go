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

func printMetadataKind(local []byte) bool {
	switch string(local) {
	case "pageSetUpPr", "printOptions", "pageMargins", "pageSetup", "headerFooter", "rowBreaks", "colBreaks", "legacyDrawingHF":
		return true
	}
	return false
}

// One explicit tag scan reaches metadata after sparse or million-row data.
// Cell text and values are neither decoded nor retained. Only bounded print
// blocks and real ancestor namespace headers reach the XML decoder.
func scanPrintSettingsXML(ctx context.Context, input io.Reader, total int64, b *printMetadataBuilder, progress func(int64, int64, string)) error {
	return scanPrintSourceXML(ctx, input, total, b, progress, nil)
}

// Geometry reuses the same bounded structural and namespace checks, without
// decoding or retaining cell text. Observers only see source opening tags.
func scanPrintSourceXML(ctx context.Context, input io.Reader, total int64, b *printMetadataBuilder, progress func(int64, int64, string), observe func([]byte, tagInfo, int) error) error {
	counted := &countingReader{r: catalogueContextReader{ctx, input}}
	r := bufio.NewReaderSize(counted, 1<<20)
	var names [formats.PrintXMLDepth][]byte
	var root, sheetPr, block, buffer []byte
	var parents [][]byte
	depth, captureDepth, dataCount := 0, 0, 0
	closed, dataClosed, inData, custom := false, false, false, false
	var mergeHeader []byte
	mergeSeen, mergeOpen, mergeExpected := false, false, -1
	last := int64(0)
	appendBlock := func(data []byte) error {
		if len(data) > formats.PrintBlockBytes-len(block) {
			return errors.New("源打印XML块超过读取容量，未发布部分设置")
		}
		block = append(block, data...)
		return nil
	}
	if progress != nil {
		progress(0, total, "正在读取源打印设置")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk, readErr := r.ReadSlice('<')
		hasTag := len(chunk) > 0 && chunk[len(chunk)-1] == '<'
		text := chunk
		if hasTag {
			text = chunk[:len(chunk)-1]
		}
		if counted.n-int64(r.Buffered())-int64(len(chunk)) == 0 {
			text = bytes.TrimPrefix(text, []byte{0xEF, 0xBB, 0xBF})
		}
		if depth == 0 && len(bytes.TrimSpace(text)) > 0 {
			return errors.New("打印设置所在工作表根外含无效文字")
		}
		if captureDepth > 0 {
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
			if captureDepth > 0 {
				if err := appendBlock(tag); err != nil {
					return err
				}
			}
			if !info.valid {
				if !bytes.HasPrefix(tag, []byte("<!--")) && !bytes.HasPrefix(tag, []byte("<![CDATA[")) && !bytes.HasPrefix(tag, []byte("<?")) {
					return errors.New("打印设置所在工作表含未知XML声明")
				}
				if bytes.HasPrefix(tag, []byte("<![CDATA[")) && depth == 0 || bytes.HasPrefix(tag, []byte("<?xml")) && len(root) != 0 {
					return errors.New("打印设置所在工作表XML声明位置无效")
				}
			} else {
				at := 1
				if info.closing {
					at++
				}
				end := at
				for end < len(tag) && !isXMLSpace(tag[end]) && tag[end] != '/' && tag[end] != '>' {
					end++
				}
				name := tag[at:end]
				if len(name) == 0 || len(name) > formats.DataRuleQNameBytes || closed {
					return errors.New("打印设置所在工作表XML根或标签名无效")
				}
				if info.closing {
					if depth == 0 || !bytes.Equal(names[depth-1], name) {
						return errors.New("打印设置所在工作表XML结束标签不匹配")
					}
					tail := end
					for tail < len(tag) && isXMLSpace(tag[tail]) {
						tail++
					}
					if tail != len(tag)-1 || tag[tail] != '>' {
						return errors.New("打印设置所在工作表XML结束标签含无效内容")
					}
					if captureDepth == depth {
						if err := readPrintSettingsBlock(ctx, parents, block, b); err != nil {
							return err
						}
						captureDepth, block, parents = 0, nil, nil
					}
					if depth == 2 {
						if mergeOpen {
							mergeOpen = false
						}
						if string(info.name) == "sheetData" && inData {
							inData, dataClosed = false, true
						}
						if string(info.name) == "sheetPr" {
							sheetPr = nil
						}
						if string(info.name) == "customSheetViews" {
							custom = false
						}
					}
					depth--
					closed = depth == 0
				} else {
					if depth >= formats.PrintXMLDepth {
						return errors.New("打印设置所在XML层级超过读取容量")
					}
					if depth == 0 {
						if len(root) != 0 || string(info.name) != "worksheet" || info.selfClosing || len(tag) > formats.PrintTextBytes {
							return errors.New("源打印工作表根无效或超过读取容量")
						}
						root = bytes.Clone(tag)
						if _, err := printSettingsHeader(nil, root); err != nil {
							return err
						}
					}
					if depth == 1 && string(info.name) == "mergeCells" {
						node, err := printSettingsHeader([][]byte{root}, tag)
						if err != nil {
							return err
						}
						if node.Name.Local != "" {
							if mergeSeen {
								return errors.New("打印工作表的合并区域容器重复")
							}
							mergeSeen, mergeOpen, mergeHeader = true, !info.selfClosing, bytes.Clone(tag)
							for _, attr := range node.Attr {
								if attr.Name.Space == "" && attr.Name.Local == "count" {
									value, err := strconv.Atoi(strings.TrimSpace(attr.Value))
									if err != nil || value < 0 || value > formats.PrintPreviewMergeLimit {
										return errors.New("打印工作表合并区域声明无效或超过读取容量")
									}
									mergeExpected = value
								}
							}
						}
					} else if mergeOpen && depth >= 2 {
						if depth != 2 || string(info.name) != "mergeCell" {
							return errors.New("打印工作表合并区域含无法解释的子结构")
						}
						node, err := printSettingsHeader([][]byte{root, mergeHeader}, tag)
						if err != nil {
							return err
						}
						ref := ""
						for _, attr := range node.Attr {
							if attr.Name.Space == "" && attr.Name.Local == "ref" {
								ref = attr.Value
							}
						}
						parts := strings.Split(ref, ":")
						if node.Name.Local == "" || len(parts) != 2 || b.merges >= formats.PrintPreviewMergeLimit {
							return errors.New("打印工作表合并区域无法完整读取")
						}
						c1, r1, err1 := ParseCellRef(parts[0])
						c2, r2, err2 := ParseCellRef(parts[1])
						if err1 != nil || err2 != nil || c2 < c1 || r2 < r1 || r2 > formats.ExcelRows || c2 > formats.ExcelColumns {
							return errors.New("打印工作表合并区域源坐标无效")
						}
						b.merges++
					}
					if depth == 1 && (string(info.name) == "sheetData" || string(info.name) == "sheetPr" || string(info.name) == "customSheetViews") {
						node, err := printSettingsHeader([][]byte{root}, tag)
						if err != nil {
							return err
						}
						if node.Name.Local != "" {
							switch string(info.name) {
							case "sheetData":
								dataCount++
								if dataCount != 1 {
									return errors.New("源打印工作表数据容器重复")
								}
								inData, dataClosed = !info.selfClosing, info.selfClosing
							case "sheetPr":
								if err := b.unique("sheetPr"); err != nil {
									return err
								}
								if !info.selfClosing {
									sheetPr = bytes.Clone(tag)
								}
							case "customSheetViews":
								custom = !info.selfClosing
							}
						}
					}
					if captureDepth == 0 && printMetadataKind(info.name) {
						var scope [][]byte
						if depth == 1 && string(info.name) != "pageSetUpPr" {
							scope = [][]byte{root}
						} else if depth == 2 && string(info.name) == "pageSetUpPr" && sheetPr != nil {
							scope = [][]byte{root, sheetPr}
						}
						var node xml.StartElement
						if scope != nil {
							node, err = printSettingsHeader(scope, tag)
							if err != nil {
								return err
							}
						}
						if node.Name.Local != "" {
							if err := b.unique(node.Name.Local); err != nil {
								return err
							}
							if err := appendBlock(tag); err != nil {
								return err
							}
							if info.selfClosing {
								if err := readPrintSettingsBlock(ctx, scope, block, b); err != nil {
									return err
								}
								block = nil
							} else {
								captureDepth, parents = depth+1, scope
							}
						} else if !inData && !custom {
							b.notice("源工作表含未知位置或命名空间的打印定义，未完整解释")
						}
					}
					if observe != nil {
						if err := observe(tag, info, depth+1); err != nil {
							return err
						}
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
			progress(current, total, "正在读取源打印设置")
			last = current
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			return readErr
		}
	}
	if !closed || depth != 0 || captureDepth != 0 || dataCount != 1 || !dataClosed {
		return errors.New("源打印工作表XML未完整结束")
	}
	if mergeExpected >= 0 && mergeExpected != b.merges {
		return errors.New("打印工作表合并区域数量与源声明不符")
	}
	return ctx.Err()
}

func validatePrintXMLToken(token xml.Token, depth int) error {
	if node, ok := token.(xml.StartElement); ok {
		limit := formats.PrintAttributes
		if depth == 0 {
			limit = formats.DataRuleRootAttributes
		}
		if depth >= formats.PrintXMLDepth || len(node.Attr) > limit {
			return errors.New("打印设置XML层级或属性超过读取容量")
		}
		seen := make(map[xml.Name]bool, len(node.Attr))
		for _, attr := range node.Attr {
			if seen[attr.Name] || len(attr.Value) > formats.PrintTextBytes {
				return errors.New("打印设置XML属性重复或文字超过读取容量")
			}
			seen[attr.Name] = true
		}
	}
	return nil
}

func printSettingsHeader(parents [][]byte, tag []byte) (xml.StartElement, error) {
	var prefix strings.Builder
	for _, parent := range parents {
		prefix.Write(parent)
	}
	d := xml.NewDecoder(io.MultiReader(strings.NewReader(prefix.String()), bytes.NewReader(tag)))
	var main string
	for depth := 0; depth <= len(parents); depth++ {
		token, err := d.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		if err := validatePrintXMLToken(token, depth); err != nil {
			return xml.StartElement{}, err
		}
		node, ok := token.(xml.StartElement)
		if !ok {
			return xml.StartElement{}, errors.New("打印设置源层级不是有效的XML元素")
		}
		if depth == 0 {
			if node.Name.Local != "worksheet" || !catalogueNamespace(node.Name.Space) {
				return xml.StartElement{}, errors.New("源打印工作表根命名空间无效")
			}
			main = node.Name.Space
		}
		if node.Name.Space != main {
			return xml.StartElement{}, nil
		}
		if depth == len(parents) {
			return node, nil
		}
	}
	return xml.StartElement{}, nil
}
