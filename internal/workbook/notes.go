package workbook

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

type SourceNote struct {
	Ref    string `json:"ref"`
	Author string `json:"author"`
	Text   string `json:"text"`
}

// DecodeNotes reads literal text runs only, retaining line breaks and spaces.
func DecodeNotes(ctx context.Context, data []byte) ([]string, []SourceNote, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var authors []string
	notes := []SourceNote{}
	seen := map[string]bool{}
	var stack []string
	var root string
	var body strings.Builder
	var note SourceNote
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		switch v := token.(type) {
		case xml.StartElement:
			if len(stack) >= 64 {
				return nil, nil, errors.New("批注 XML 层级超限")
			}
			if len(stack) == 0 {
				if root != "" || v.Name.Local != "comments" {
					return nil, nil, errors.New("批注部件无效")
				}
				root = v.Name.Space
			}
			name := v.Name.Local
			if v.Name.Space != root {
				name = ""
			}
			stack = append(stack, name)
			switch strings.Join(stack, "/") {
			case "comments/authors/author":
				body.Reset()
			case "comments/commentList/comment":
				note = SourceNote{}
				author := -1
				for _, a := range v.Attr {
					if a.Name.Space != "" {
						continue
					}
					switch a.Name.Local {
					case "ref":
						note.Ref = a.Value
					case "authorId":
						author, err = strconv.Atoi(a.Value)
						if err != nil {
							return nil, nil, err
						}
					}
				}
				col, row, e := ParseCellRef(note.Ref)
				if e != nil || row > formats.ExcelRows || col > formats.ExcelColumns || note.Ref != ColumnName(col)+strconv.Itoa(row) || seen[note.Ref] || author < 0 || author >= len(authors) {
					return nil, nil, errors.New("批注作者或坐标无效")
				}
				seen[note.Ref] = true
				note.Author = authors[author]
				body.Reset()
			}
		case xml.CharData:
			switch strings.Join(stack, "/") {
			case "comments/authors/author", "comments/commentList/comment/text/t", "comments/commentList/comment/text/r/t":
				body.Write(v)
			}
		case xml.EndElement:
			switch strings.Join(stack, "/") {
			case "comments/authors/author":
				authors = append(authors, body.String())
			case "comments/commentList/comment":
				note.Text = body.String()
				notes = append(notes, note)
				if len(notes) > formats.EditXLSXRangeLimit {
					return nil, nil, errors.New("批注数量超限")
				}
			}
			stack = stack[:len(stack)-1]
		}
	}
	if root == "" {
		return nil, nil, errors.New("缺少批注部件")
	}
	return authors, notes, nil
}
func (w *Workbook) SheetNotes(ctx context.Context, meta SheetMeta) ([]SourceNote, bool, error) {
	if w.Format == "xlsb" {
		return w.xlsbNotes(ctx, meta)
	}
	reader, err := w.openCatalogueReader(ctx)
	if err != nil {
		return nil, false, err
	}
	defer reader.zip.Close()
	rels := relsPart(meta.Path)
	if reader.entries[rels] == nil {
		return []SourceNote{}, false, nil
	}
	raw, err := reader.part(rels)
	if err != nil {
		return nil, false, err
	}
	links, err := catalogueRelationshipsXML(raw)
	if err != nil {
		return nil, false, err
	}
	name := ""
	threaded := false
	for _, link := range links {
		if strings.Contains(strings.ToLower(link.Type), "threadedcomment") {
			threaded = true
		}
		if !catalogueRelationType(link.Type, "comments") {
			continue
		}
		if name != "" || !catalogueInternalRelation(link) {
			return nil, false, errors.New("批注关系不唯一或指向外部")
		}
		name, err = catalogueRelationTarget(meta.Path, link.Target)
		if err != nil {
			return nil, false, err
		}
	}
	if name == "" {
		return []SourceNote{}, threaded, nil
	}
	raw, err = reader.part(name)
	if err != nil {
		return nil, false, fmt.Errorf("读取批注：%w", err)
	}
	_, notes, err := DecodeNotes(ctx, raw)
	return notes, threaded, err
}
