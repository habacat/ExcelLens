package workbook

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// ReadCatalogue reads source metadata on demand. It does not open worksheet
// bodies, build indexes, infer named regions or execute formulas. A table part
// reached only from a relationship remains an unverified candidate; a later
// explicit verification must check the worksheet's own tableParts declaration.
func (w *Workbook) ReadCatalogue(ctx context.Context) (Catalogue, error) {
	if err := ctx.Err(); err != nil {
		return Catalogue{}, err
	}
	b := newCatalogueBuilder()
	capability, ok := formats.Lookup(w.Format)
	if !ok || !capability.CatalogueMetadata {
		return b.value, nil
	}
	if w.source != nil {
		if err := w.CheckSourceUnchanged(); err != nil {
			return Catalogue{}, err
		}
		if w.Format == "xlsb" {
			value, err := w.xlsbCatalogue(ctx, true)
			if err != nil {
				return Catalogue{}, err
			}
			return w.resolveCatalogue(ctx, value)
		}
		if w.source.xlsCatalogue == nil {
			return Catalogue{}, errors.New("源适配器未提供名称目录，不能按其他格式推断")
		}
		return w.readBIFFCatalogue(ctx, "", nil)
	}
	reader, err := w.openCatalogueReader(ctx)
	if err != nil {
		return Catalogue{}, err
	}
	defer reader.zip.Close()
	wbXML, err := reader.part("xl/workbook.xml")
	if err != nil {
		return Catalogue{}, err
	}
	original, err := catalogueSheetsXML(wbXML)
	if err != nil {
		return Catalogue{}, err
	}
	names, err := catalogueNamesXML(wbXML, original)
	if err != nil {
		return Catalogue{}, err
	}
	b.value.Complete, b.value.Notices = names.Complete, names.Notices
	b.value.NamesIncomplete = names.NamesIncomplete
	for _, item := range names.Items {
		if !b.add(item) {
			break
		}
	}
	relsXML, err := reader.part("xl/_rels/workbook.xml.rels")
	if err != nil {
		return Catalogue{}, err
	}
	relations, err := catalogueRelationshipsXML(relsXML)
	if err != nil {
		return Catalogue{}, err
	}
	relByID := make(map[string]relationship, len(relations))
	for _, rel := range relations {
		relByID[rel.ID] = rel
	}
	sheetParts := make(map[string]string)
	for _, sheet := range original {
		rel, found := relByID[sheet.RelID]
		if !found || !catalogueRelationType(rel.Type, "worksheet") || !catalogueInternalRelation(rel) {
			continue
		}
		part, err := catalogueRelationTarget("xl/workbook.xml", rel.Target)
		if err == nil && reader.entries[part] != nil {
			sheetParts[sheet.Name] = part
		}
	}
	for sheetIndex, meta := range w.Sheets {
		if err := ctx.Err(); err != nil {
			return Catalogue{}, err
		}
		if sheetParts[meta.Name] != meta.Path {
			return Catalogue{}, errors.New("工作表目录与真实工作簿关系不一致，不能发布名称定位")
		}
		part := path.Join(path.Dir(meta.Path), "_rels", path.Base(meta.Path)+".rels")
		if reader.entries[part] == nil {
			continue
		}
		data, err := reader.part(part)
		if err != nil {
			b.notice("部分工作表关系未完整读取：" + err.Error())
			continue
		}
		relations, err := catalogueRelationshipsXML(data)
		if err != nil {
			b.notice("部分工作表关系无效：" + err.Error())
			continue
		}
		for ordinal, rel := range relations {
			if !catalogueRelationType(rel.Type, "table") {
				continue
			}
			if err := ctx.Err(); err != nil {
				return Catalogue{}, err
			}
			item := CatalogueItem{Kind: "table", Sheet: meta.Name, Status: "unavailable", Definition: rel.Target, Table: &CatalogueTable{RelationID: rel.ID}}
			if !catalogueInternalRelation(rel) {
				item.Reason = "外部或未知目标模式的表对象关系不读取或定位"
			} else if target, targetErr := catalogueRelationTarget(meta.Path, rel.Target); targetErr != nil {
				item.Status, item.Reason = "invalid", targetErr.Error()
			} else {
				item.Table.Part = target
				data, err := reader.part(target)
				if err != nil {
					item.Reason = "表对象定义未完整读取：" + err.Error()
					b.notice("部分表对象定义未完整读取")
				} else {
					item, err = ParseTableDefinition(data, meta.Name, target, rel.ID)
					if err != nil {
						item.Status, item.Reason = "invalid", "表对象定义无效："+err.Error()
						b.notice("部分表对象定义无效")
					}
				}
			}
			item.ID = "table-" + strconv.Itoa(sheetIndex+1) + "-" + strconv.Itoa(ordinal+1)
			if !b.add(item) {
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Catalogue{}, err
	}
	if err := w.CheckSourceUnchanged(); err != nil {
		return Catalogue{}, err
	}
	w.applyKnownWorksheetDeclarations(&b.value, reader)
	return w.resolveCatalogue(ctx, b.value)
}

type catalogueReader struct {
	ctx     context.Context
	zip     *zip.ReadCloser
	entries map[string]*zip.File
	bytes   int
}

func (w *Workbook) openCatalogueReader(ctx context.Context) (*catalogueReader, error) {
	zr, err := zip.OpenReader(w.Path)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = zr.Close()
		}
	}()
	if len(zr.File) > maxArchiveEntries || fingerprintZip(zr.File) != w.Fingerprint {
		return nil, errors.New("源工作簿在打开后发生了变化，请重新打开后读取目录")
	}
	entries := make(map[string]*zip.File, len(zr.File))
	for _, file := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := path.Clean(strings.ReplaceAll(file.Name, "\\", "/"))
		if name == "." || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			continue
		}
		if entries[name] != nil {
			return nil, errors.New("工作簿含重复部件路径，不能确定唯一的名称及表对象来源")
		}
		entries[name] = file
	}
	valid = true
	return &catalogueReader{ctx: ctx, zip: zr, entries: entries}, nil
}

type catalogueContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r catalogueContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(data)
}

func (r *catalogueReader) part(name string) ([]byte, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	file := r.entries[name]
	if file == nil {
		return nil, fmt.Errorf("目录缺少部件 %q", name)
	}
	if r.bytes >= formats.CatalogueBytes {
		return nil, errors.New("目录本次元数据读取容量已用完")
	}
	limit := min(formats.CataloguePartBytes, formats.CatalogueBytes-r.bytes)
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("目录部件 %q 超过单部件或本次总读取容量", name)
	}
	source, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer source.Close()
	data, err := io.ReadAll(io.LimitReader(catalogueContextReader{r.ctx, source}, int64(limit)+1))
	r.bytes += len(data)
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("目录解压后的实际元数据超过读取容量")
	}
	return data, r.ctx.Err()
}

func catalogueRelationType(value, kind string) bool {
	return value == "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"+kind || value == "http://purl.oclc.org/ooxml/officeDocument/relationships/"+kind
}

func catalogueInternalRelation(rel relationship) bool {
	return rel.TargetMode == "" || strings.EqualFold(rel.TargetMode, "Internal")
}

func catalogueRelationTarget(source, target string) (string, error) {
	u, err := url.Parse(strings.ReplaceAll(target, "\\", "/"))
	if err != nil || u == nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(target, "#\x00") {
		return "", errors.New("表对象部件关系目标不是工作簿内的可靠路径")
	}
	value := u.Path
	if value == "" {
		return "", errors.New("表对象部件关系目标为空")
	}
	if strings.HasPrefix(value, "/") {
		value = strings.TrimPrefix(value, "/")
	} else {
		value = path.Join(path.Dir(source), value)
	}
	value = path.Clean(value)
	if value == "." || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\\x00") {
		return "", errors.New("表对象部件关系超出工作簿内部路径")
	}
	return value, nil
}

func catalogueRelationshipsXML(data []byte) ([]relationship, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, namespace := 0, ""
	root := false
	result := []relationship{}
	seen := make(map[string]bool)
	for {
		token, err := d.Token()
		if err == io.EOF {
			if !root || depth != 0 {
				return nil, errors.New("目录部件关系XML未完整结束")
			}
			return result, nil
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
			if depth == 1 {
				if root || node.Name.Local != "Relationships" || node.Name.Space != "" && node.Name.Space != "http://schemas.openxmlformats.org/package/2006/relationships" {
					return nil, errors.New("目录部件关系根或命名空间无效")
				}
				root, namespace = true, node.Name.Space
			}
			if depth != 2 || node.Name.Local != "Relationship" || node.Name.Space != namespace {
				continue
			}
			rel := relationship{}
			for _, attr := range node.Attr {
				if attr.Name.Space != "" {
					continue
				}
				switch attr.Name.Local {
				case "Id":
					rel.ID = attr.Value
				case "Type":
					rel.Type = attr.Value
				case "Target":
					rel.Target = attr.Value
				case "TargetMode":
					rel.TargetMode = attr.Value
				}
			}
			if rel.ID == "" || rel.Type == "" || rel.Target == "" || seen[rel.ID] || len(result) >= formats.CatalogueEntries {
				return nil, errors.New("目录部件关系缺少标识、存在重复编号或超过容量")
			}
			seen[rel.ID] = true
			result = append(result, rel)
		case xml.EndElement:
			depth--
		}
	}
}

func catalogueSheetsXML(data []byte) ([]workbookSheetXML, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, namespace := 0, ""
	root, container := false, false
	result := []workbookSheetXML{}
	seen := make(map[string]bool)
	for {
		token, err := d.Token()
		if err == io.EOF {
			if !root || depth != 0 {
				return nil, errors.New("目录工作表结构XML未完整结束")
			}
			return result, nil
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
			if depth == 1 {
				if root || node.Name.Local != "workbook" || !catalogueNamespace(node.Name.Space) {
					return nil, errors.New("目录工作表结构根或命名空间无效")
				}
				root, namespace = true, node.Name.Space
			}
			if depth == 2 {
				container = node.Name.Local == "sheets" && node.Name.Space == namespace
			}
			if depth != 3 || !container || node.Name.Local != "sheet" || node.Name.Space != namespace {
				continue
			}
			sheet := workbookSheetXML{}
			for _, attr := range node.Attr {
				if attr.Name.Space == "" {
					switch attr.Name.Local {
					case "name":
						sheet.Name = attr.Value
					case "state":
						sheet.State = attr.Value
					}
				} else if attr.Name.Local == "id" && (attr.Name.Space == "http://schemas.openxmlformats.org/officeDocument/2006/relationships" || attr.Name.Space == "http://purl.oclc.org/ooxml/officeDocument/relationships") {
					sheet.RelID = attr.Value
				}
			}
			key := strings.ToLower(sheet.Name)
			if sheet.Name == "" || sheet.RelID == "" || seen[key] || len(result) >= formats.CatalogueEntries {
				return nil, errors.New("目录工作表名称或关系无效、重名或超过容量")
			}
			seen[key] = true
			result = append(result, sheet)
		case xml.EndElement:
			if depth == 2 {
				container = false
			}
			depth--
		}
	}
}
