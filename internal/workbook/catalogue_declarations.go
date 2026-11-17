package workbook

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// Captured by the existing metadata tag pass. Only the two namespace headers
// and tableParts byte ranges are retained, independently of the used rows.
// This transient seed is excluded from SourceLayout's versioned JSON contract.
type worksheetCatalogueSeed struct {
	root, data, rootQName, dataQName string
	rootClosed, dataClosed           bool
	dataCount                        int
	blockOpen                        bool
	blockStart                       int64
	blocks                           []SourceXMLRange
	notice                           string
}

func catalogueTagQName(tag []byte) string {
	start := 1
	if len(tag) > 1 && tag[1] == '/' {
		start++
	}
	end := start
	for end < len(tag) && !bytes.ContainsRune([]byte(" \t\r\n/>"), rune(tag[end])) {
		end++
	}
	return string(tag[start:end])
}

func (s *worksheetCatalogueSeed) captureToken(tag []byte, info tagInfo, start, end int64, level int) {
	if s.notice != "" || !info.valid {
		return
	}
	if level < 1 || s.rootClosed {
		s.notice = "表声明所在工作表的根结构无效"
		return
	}
	if level == 1 {
		if info.closing {
			if string(info.name) != "worksheet" || catalogueTagQName(tag) != s.rootQName || s.blockOpen {
				s.notice = "工作表根结束标签或表声明未完整结束"
			} else {
				s.rootClosed = true
			}
		} else if s.root != "" || string(info.name) != "worksheet" || info.selfClosing || len(tag) > formats.CatalogueRootBytes {
			s.notice = "表声明的工作表根无效或超过读取容量"
		} else {
			s.root, s.rootQName = string(tag), catalogueTagQName(tag)
		}
		return
	}
	if level != 2 {
		return
	}
	switch string(info.name) {
	case "sheetData":
		if info.closing {
			if s.dataCount != 1 || s.dataClosed || catalogueTagQName(tag) != s.dataQName {
				s.notice = "工作表数据结束标签无法与源声明对应"
			} else {
				s.dataClosed = true
			}
		} else {
			s.dataCount++
			if s.dataCount != 1 || len(tag) > formats.CatalogueRootBytes {
				s.notice = "工作表数据声明重复或超过读取容量"
			} else {
				s.data, s.dataQName, s.dataClosed = string(tag), catalogueTagQName(tag), info.selfClosing
			}
		}
	case "tableParts":
		if !s.dataClosed {
			s.notice = "表声明未位于真实工作表数据之后"
			return
		}
		if !info.closing {
			if s.blockOpen || len(s.blocks) > 0 {
				s.notice = "工作表含重复的表声明容器"
				return
			}
			s.blockStart, s.blockOpen = start, true
		}
		if info.closing || info.selfClosing {
			if !s.blockOpen || end <= s.blockStart || end-s.blockStart > formats.CataloguePartBytes {
				s.notice = "表声明范围无效、截断或超过读取容量"
				return
			}
			s.blocks = append(s.blocks, SourceXMLRange{s.blockStart, end})
			s.blockOpen = false
		}
	}
}

type worksheetCatalogueDeclarations struct {
	IDs []string `json:"ids"`
}

func (s *worksheetCatalogueSeed) namespace() (string, error) {
	if s == nil || s.notice != "" || s.root == "" || !s.rootClosed || s.dataCount != 1 || !s.dataClosed || s.blockOpen {
		message := "工作表的表声明结构未完整确认"
		if s != nil && s.notice != "" {
			message = s.notice
		}
		return "", errors.New(message)
	}
	d := xml.NewDecoder(strings.NewReader(s.root + s.data))
	var namespace string
	for depth, local := range []string{"worksheet", "sheetData"} {
		token, err := d.Token()
		if err != nil {
			return "", err
		}
		if err := validateCatalogueXMLToken(token, depth); err != nil {
			return "", err
		}
		node, ok := token.(xml.StartElement)
		if !ok || node.Name.Local != local || !catalogueNamespace(node.Name.Space) || depth == 1 && node.Name.Space != namespace {
			return "", errors.New("表声明的工作表根或数据命名空间无效")
		}
		namespace = node.Name.Space
	}
	return namespace, nil
}

func readWorksheetTableBlock(ctx context.Context, seed *worksheetCatalogueSeed, data []byte, namespace string) ([]string, error) {
	d := xml.NewDecoder(io.MultiReader(strings.NewReader(seed.root), bytes.NewReader(data), strings.NewReader("</"+seed.rootQName+">")))
	depth, expected, containers := 0, -1, 0
	ids := []string{}
	seen := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := d.Token()
		if err == io.EOF {
			if depth != 0 || containers != 1 || expected >= 0 && expected != len(ids) {
				return nil, errors.New("表声明未完整结束或数量不一致")
			}
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		if err := validateCatalogueXMLToken(token, depth); err != nil {
			return nil, err
		}
		switch node := token.(type) {
		case xml.StartElement:
			depth++
			if node.Name.Space != namespace {
				return nil, errors.New("表声明含非源工作表命名空间的节点")
			}
			switch depth {
			case 1:
				if node.Name.Local != "worksheet" {
					return nil, errors.New("表声明根无效")
				}
			case 2:
				containers++
				if node.Name.Local != "tableParts" || containers != 1 {
					return nil, errors.New("表声明不是工作表的唯一直接子容器")
				}
				for _, attr := range node.Attr {
					if attr.Name.Space == "" && attr.Name.Local == "count" {
						n, err := strconv.ParseUint(attr.Value, 10, 32)
						if err != nil || n > formats.CatalogueEntries {
							return nil, errors.New("表声明数量无效或超过容量")
						}
						expected = int(n)
					}
				}
			case 3:
				if node.Name.Local != "tablePart" {
					return nil, errors.New("表声明含尚未支持的子节点")
				}
				id := ""
				for _, attr := range node.Attr {
					if attr.Name.Local == "id" && (attr.Name.Space == "http://schemas.openxmlformats.org/officeDocument/2006/relationships" || attr.Name.Space == "http://purl.oclc.org/ooxml/officeDocument/relationships") {
						id = attr.Value
					}
				}
				if id == "" || len(id) > formats.CatalogueDefinitionMax || seen[id] || len(ids) >= formats.CatalogueEntries {
					return nil, errors.New("表声明关系编号缺失、重复或超过容量")
				}
				seen[id], ids = true, append(ids, id)
			default:
				return nil, errors.New("表声明中含嵌套结构，不能确认真实关系")
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if strings.TrimSpace(string(node)) != "" {
				return nil, errors.New("表声明中含无法解释的文字内容")
			}
		}
	}
}

func worksheetDeclarationsFromSeed(ctx context.Context, idx *SheetIndex, seed *worksheetCatalogueSeed) (worksheetCatalogueDeclarations, error) {
	value := worksheetCatalogueDeclarations{IDs: []string{}}
	namespace, err := seed.namespace()
	if err != nil {
		return value, err
	}
	if len(seed.blocks) == 0 {
		return value, nil
	}
	if len(seed.blocks) != 1 {
		return value, errors.New("工作表的表声明容器数量无效")
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return value, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() != idx.XMLSize {
		return value, errors.New("表声明的XML缓存与索引大小不一致")
	}
	block := seed.blocks[0]
	if block.Start < 0 || block.End <= block.Start || block.End > idx.XMLSize || block.End-block.Start > formats.CataloguePartBytes {
		return value, errors.New("表声明缓存范围无效或超过容量")
	}
	data, err := io.ReadAll(catalogueContextReader{ctx, io.NewSectionReader(f, block.Start, block.End-block.Start)})
	if err != nil {
		return value, err
	}
	value.IDs, err = readWorksheetTableBlock(ctx, seed, data, namespace)
	return value, err
}

// Only an explicit confirmation of an older index uses this scan. No cell
// values are decoded. Its caller verifies the full source part CRC in one pass.
func scanWorksheetCatalogue(ctx context.Context, input io.Reader, total int64, progress func(int64, int64, string)) (*worksheetCatalogueSeed, error) {
	seed := new(worksheetCatalogueSeed)
	counted := &countingReader{r: input}
	r := bufio.NewReaderSize(counted, 1<<20)
	depth := 0
	var buffer []byte
	last := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunk, readErr := r.ReadSlice('<')
		current := counted.n - int64(r.Buffered())
		text := chunk
		if len(text) > 0 && text[len(text)-1] == '<' {
			text = text[:len(text)-1]
		}
		if seed.rootClosed && len(bytes.TrimSpace(text)) > 0 {
			seed.notice = "工作表根结束后含无效文字，表声明未确认"
		}
		if len(chunk) > 0 && chunk[len(chunk)-1] == '<' {
			start := current - 1
			tag, err := readXMLTag(r, buffer)
			if err != nil {
				return nil, err
			}
			buffer = tag
			info := inspectXMLTag(tag)
			if info.valid {
				level := depth + 1
				if info.closing {
					level = depth
					depth--
				} else if !info.selfClosing {
					depth++
				}
				seed.captureToken(tag, info, start, counted.n-int64(r.Buffered()), level)
			}
		}
		if progress != nil && current-last >= 8<<20 {
			progress(current, total, "正在确认旧索引的表对象声明")
			last = current
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			return nil, fmt.Errorf("读取表声明失败：%w", readErr)
		}
	}
	return seed, ctx.Err()
}
