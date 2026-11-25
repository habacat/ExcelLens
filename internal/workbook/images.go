package workbook

// Images are read independently of the cell index. OOXML drawings contain
// coordinates in EMUs (English Metric Units); 9,525 EMU = one CSS pixel at
// Excel's nominal 96 DPI. All offsets are kept as floats until layout time.
// No image bytes are decompressed while opening a workbook or scrolling.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
)

const (
	maxDrawingXML       = 32 << 20
	maxPictureBytes     = 32 << 20
	maxPicturesPerSheet = 4096
	emuPerPixel         = 9525.0
)

type ImageMarker struct {
	Col    int     `json:"col"`    // zero-based worksheet column
	Row    int     `json:"row"`    // zero-based worksheet row
	ColOff float64 `json:"colOff"` // CSS pixels, not EMUs
	RowOff float64 `json:"rowOff"`
}

type ImageCrop struct {
	Left   float64 `json:"left,omitempty"`
	Top    float64 `json:"top,omitempty"`
	Right  float64 `json:"right,omitempty"`
	Bottom float64 `json:"bottom,omitempty"`
}

type EmbeddedImage struct {
	ID           int          `json:"id"`
	Name         string       `json:"name,omitempty"`
	Description  string       `json:"description,omitempty"`
	Kind         string       `json:"kind"` // oneCell / twoCell / absolute
	EditAs       string       `json:"editAs,omitempty"`
	HasTransform bool         `json:"hasTransform,omitempty"`
	From         ImageMarker  `json:"from"`
	To           *ImageMarker `json:"to,omitempty"`
	Width        float64      `json:"width,omitempty"`
	Height       float64      `json:"height,omitempty"`
	X            float64      `json:"x,omitempty"`
	Y            float64      `json:"y,omitempty"`
	Crop         ImageCrop    `json:"crop"`
	Rotation     float64      `json:"rotation,omitempty"`
	FlipH        bool         `json:"flipH,omitempty"`
	FlipV        bool         `json:"flipV,omitempty"`
	Part         string       `json:"-"` // never permit clients to name ZIP entries
}

type drawingMarkerXML struct {
	Col    int   `xml:"col"`
	Row    int   `xml:"row"`
	ColOff int64 `xml:"colOff"`
	RowOff int64 `xml:"rowOff"`
}
type drawingRectXML struct {
	X  int64 `xml:"x,attr"`
	Y  int64 `xml:"y,attr"`
	CX int64 `xml:"cx,attr"`
	CY int64 `xml:"cy,attr"`
}
type drawingPicXML struct {
	NV struct {
		Prop struct {
			Name  string `xml:"name,attr"`
			Descr string `xml:"descr,attr"`
			Title string `xml:"title,attr"`
		} `xml:"cNvPr"`
	} `xml:"nvPicPr"`
	Fill struct {
		Blip struct {
			Embed string `xml:"embed,attr"`
			Link  string `xml:"link,attr"`
		} `xml:"blip"`
		Crop struct {
			L int64 `xml:"l,attr"`
			T int64 `xml:"t,attr"`
			R int64 `xml:"r,attr"`
			B int64 `xml:"b,attr"`
		} `xml:"srcRect"`
	} `xml:"blipFill"`
	Shape struct {
		Transform struct {
			Rot   int64          `xml:"rot,attr"`
			FlipH bool           `xml:"flipH,attr"`
			FlipV bool           `xml:"flipV,attr"`
			Off   drawingRectXML `xml:"off"`
			Ext   drawingRectXML `xml:"ext"`
		} `xml:"xfrm"`
	} `xml:"spPr"`
}
type drawingAnchorXML struct {
	From drawingMarkerXML `xml:"from"`
	To   drawingMarkerXML `xml:"to"`
	Ext  drawingRectXML   `xml:"ext"`
	Pos  drawingRectXML   `xml:"pos"`
	Pic  drawingPicXML    `xml:"pic"`
}

func markerFromXML(m drawingMarkerXML) ImageMarker {
	return ImageMarker{Col: m.Col, Row: m.Row, ColOff: float64(m.ColOff) / emuPerPixel, RowOff: float64(m.RowOff) / emuPerPixel}
}
func validateMarker(m ImageMarker) bool {
	return m.Col >= 0 && m.Col < 16384 && m.Row >= 0 && m.Row < 1048576 && m.ColOff >= -100000 && m.ColOff <= 100000 && m.RowOff >= -100000 && m.RowOff <= 100000
}

func isPictureType(part string) bool {
	switch strings.ToLower(path.Ext(part)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".tif", ".tiff", ".emf", ".wmf":
		return true
	}
	return false
}

// OOXML relationships use targets relative to the owning part, or absolute
// package paths beginning with '/'. TargetMode=External is forbidden.
func resolvePackagePart(owner, target string) (string, bool) {
	target = strings.ReplaceAll(target, "\\", "/")
	if decoded, err := url.PathUnescape(target); err == nil {
		target = decoded
	}
	if target == "" || strings.ContainsAny(target, "?#:") || strings.HasPrefix(target, "//") {
		return "", false
	}
	var name string
	if strings.HasPrefix(target, "/") {
		name = path.Clean(strings.TrimPrefix(target, "/"))
	} else {
		name = path.Clean(path.Join(path.Dir(owner), target))
	}
	return name, name != "." && !strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/")
}
func relsPart(owner string) string {
	return path.Join(path.Dir(owner), "_rels", path.Base(owner)+".rels")
}

// Find only real <drawing r:id="..."/> references, not every .rels entry:
// external/unused drawings may be present in repaired third-party workbooks.
// Worksheet drawing references normally live near the very end of sheet XML;
// the bounded tail probe avoids rereading a multi-GB XML. Fall back to a
// bounded-memory sequential scan when a valid drawing relationship exists.
var drawingTag = regexp.MustCompile(`(?i)<(?:[A-Za-z_][A-Za-z0-9_.-]*:)?drawing\s+[^>]{0,1000}>`)
var drawingIDAttr = regexp.MustCompile(`(?:^|\s)(?:[A-Za-z_][\w.-]*:)?id\s*=\s*["']([^"']+)["']`)

func drawingIDs(buf []byte, ids map[string]bool) {
	for _, tag := range drawingTag.FindAll(buf, -1) {
		if m := drawingIDAttr.FindSubmatch(tag); len(m) > 1 {
			ids[string(m[1])] = true
		}
	}
}
func referencedDrawingIDs(xmlPath string) (map[string]bool, error) {
	f, err := os.Open(xmlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const probe int64 = 2 << 20
	start := st.Size() - probe
	if start < 0 {
		start = 0
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	tail, err := io.ReadAll(io.LimitReader(f, probe))
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool)
	drawingIDs(tail, ids)
	if len(ids) > 0 || start == 0 {
		return ids, nil
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	// Constant memory even if a worksheet has hundreds of millions of cells.
	buf := make([]byte, 64<<10)
	overlap := []byte{}
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			block := append(overlap, buf[:n]...)
			drawingIDs(block, ids)
			if len(block) > 1152 {
				overlap = append(overlap[:0], block[len(block)-1152:]...)
			} else {
				overlap = append(overlap[:0], block...)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return ids, nil
}

func (w *Workbook) SheetImages(meta SheetMeta) ([]EmbeddedImage, error) {
	w.imageMu.Lock()
	defer w.imageMu.Unlock()
	if images, ok := w.imageCache[meta.Path]; ok {
		return append([]EmbeddedImage(nil), images...), nil
	}
	images, err := w.loadSheetImages(meta)
	if err == nil {
		if w.imageCache == nil {
			w.imageCache = make(map[string][]EmbeddedImage)
		}
		w.imageCache[meta.Path] = images
	}
	return append([]EmbeddedImage(nil), images...), err
}

func (w *Workbook) loadSheetImages(meta SheetMeta) ([]EmbeddedImage, error) {
	zr, err := zip.OpenReader(w.Path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	entries := make(map[string]*zip.File, len(zr.File))
	for _, entry := range zr.File {
		entries[path.Clean(strings.ReplaceAll(entry.Name, "\\", "/"))] = entry
	}
	sheetRels := relsPart(meta.Path)
	if entries[sheetRels] == nil {
		return nil, nil
	}
	raw, err := readZipEntry(entries, sheetRels, maxMetadataPart)
	if err != nil {
		return nil, err
	}
	var rels relationshipsXML
	if err = xml.Unmarshal(raw, &rels); err != nil {
		return nil, err
	}
	drawings := make(map[string]string)
	for _, rel := range rels.Relationships {
		if strings.EqualFold(rel.TargetMode, "External") || !strings.HasSuffix(rel.Type, "/drawing") {
			continue
		}
		target, valid := resolvePackagePart(meta.Path, rel.Target)
		if valid && strings.HasPrefix(target, "xl/drawings/") && entries[target] != nil {
			drawings[rel.ID] = target
		}
	}
	if len(drawings) == 0 {
		return nil, nil
	}
	idx := w.GetCachedIndex(meta.Path)
	if idx == nil {
		return nil, errors.New("工作表尚未建立索引")
	}
	var ids map[string]bool
	if w.Format == "xlsb" {
		source, err := w.xlsbSheetMeta(context.Background(), meta)
		if err != nil {
			return nil, err
		}
		ids = map[string]bool{source.Drawing: source.Drawing != ""}
	} else {
		ids, err = referencedDrawingIDs(idx.XMLPath)
		if err != nil {
			return nil, err
		}
	}
	all := make([]EmbeddedImage, 0)
	// Relationship order is not defined, but the XML drawing sequence is.
	// A worksheet normally references exactly one drawing part.
	var selected []string
	for _, rel := range rels.Relationships {
		if ids[rel.ID] && drawings[rel.ID] != "" {
			selected = append(selected, drawings[rel.ID])
		}
	}
	for _, drawingPart := range selected {
		data, err := readZipEntry(entries, drawingPart, maxDrawingXML)
		if err != nil {
			return nil, fmt.Errorf("读取绘图信息：%w", err)
		}
		drawingRels := make(map[string]string)
		if f := entries[relsPart(drawingPart)]; f != nil {
			relRaw, err := readZipEntry(entries, relsPart(drawingPart), maxMetadataPart)
			if err != nil {
				return nil, err
			}
			var dr relationshipsXML
			if err = xml.Unmarshal(relRaw, &dr); err != nil {
				return nil, err
			}
			for _, rel := range dr.Relationships {
				if strings.EqualFold(rel.TargetMode, "External") || !strings.HasSuffix(rel.Type, "/image") {
					continue
				}
				target, valid := resolvePackagePart(drawingPart, rel.Target)
				// OOXML image parts usually live under xl/media/, but valid
				// producers may use another internal package directory.
				// Resolve only by a drawing's explicit relationship; byte
				// signatures and upload limits are checked on media access.
				if valid && entries[target] != nil && isPictureType(target) {
					drawingRels[rel.ID] = target
				}
			}
		}
		dec := xml.NewDecoder(bytes.NewReader(data))
		for {
			token, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("绘图 XML 无效：%w", err)
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			kind := start.Name.Local
			editAs := ""
			for _, a := range start.Attr {
				if a.Name.Local == "editAs" {
					editAs = a.Value
					break
				}
			}
			if kind != "oneCellAnchor" && kind != "twoCellAnchor" && kind != "absoluteAnchor" {
				continue
			}
			var anchor drawingAnchorXML
			if err = dec.DecodeElement(&anchor, &start); err != nil {
				return nil, err
			}
			// A linked picture must not trigger network requests or be confused
			// with an embedded picture. Only resolved r:embed entries count.
			part := drawingRels[anchor.Pic.Fill.Blip.Embed]
			if part == "" {
				continue
			}
			from := markerFromXML(anchor.From)
			if kind != "absoluteAnchor" && !validateMarker(from) {
				continue
			}
			picture := EmbeddedImage{ID: len(all), Name: anchor.Pic.NV.Prop.Name, Description: anchor.Pic.NV.Prop.Descr, Kind: strings.TrimSuffix(kind, "Anchor"), EditAs: editAs, From: from, Part: part,
				Crop: ImageCrop{Left: float64(anchor.Pic.Fill.Crop.L) / 100000, Top: float64(anchor.Pic.Fill.Crop.T) / 100000,
					Right: float64(anchor.Pic.Fill.Crop.R) / 100000, Bottom: float64(anchor.Pic.Fill.Crop.B) / 100000},
				Rotation: float64(anchor.Pic.Shape.Transform.Rot) / 60000, FlipH: anchor.Pic.Shape.Transform.FlipH, FlipV: anchor.Pic.Shape.Transform.FlipV}
			if picture.Description == "" {
				picture.Description = anchor.Pic.NV.Prop.Title
			}
			switch kind {
			case "twoCellAnchor":
				to := markerFromXML(anchor.To)
				if !validateMarker(to) || to.Row < from.Row || (to.Row == from.Row && to.RowOff <= from.RowOff) || to.Col < from.Col || (to.Col == from.Col && to.ColOff <= from.ColOff) {
					continue
				}
				picture.To = &to
				if editAs == "oneCell" || editAs == "absolute" {
					picture.HasTransform = anchor.Pic.Shape.Transform.Ext.CX > 0 && anchor.Pic.Shape.Transform.Ext.CY > 0
					picture.Width = float64(anchor.Pic.Shape.Transform.Ext.CX) / emuPerPixel
					picture.Height = float64(anchor.Pic.Shape.Transform.Ext.CY) / emuPerPixel
					if editAs == "absolute" {
						picture.X = float64(anchor.Pic.Shape.Transform.Off.X) / emuPerPixel
						picture.Y = float64(anchor.Pic.Shape.Transform.Off.Y) / emuPerPixel
					}
				}
			case "oneCellAnchor":
				picture.Width = float64(anchor.Ext.CX) / emuPerPixel
				picture.Height = float64(anchor.Ext.CY) / emuPerPixel
			case "absoluteAnchor":
				picture.X = float64(anchor.Pos.X) / emuPerPixel
				picture.Y = float64(anchor.Pos.Y) / emuPerPixel
				picture.Width = float64(anchor.Ext.CX) / emuPerPixel
				picture.Height = float64(anchor.Ext.CY) / emuPerPixel
			}
			if kind != "twoCellAnchor" && (picture.Width <= 0 || picture.Height <= 0 || picture.Width > 100000 || picture.Height > 100000) {
				continue
			}
			all = append(all, picture)
			if len(all) > maxPicturesPerSheet {
				return nil, fmt.Errorf("工作表图片超过安全上限 %d", maxPicturesPerSheet)
			}
		}
	}
	return all, nil
}

// PictureBytes validates the media type based on the actual bytes rather than
// trusting the ZIP name, extension or content-type declaration. Unsupported
// Windows-only EMF/WMF (or TIFF) can be reported but never served as HTML.
func (w *Workbook) PictureRaw(meta SheetMeta, id int) ([]byte, string, error) {
	images, err := w.SheetImages(meta)
	if err != nil {
		return nil, "", err
	}
	if id < 0 || id >= len(images) {
		return nil, "", os.ErrNotExist
	}
	zr, err := zip.OpenReader(w.Path)
	if err != nil {
		return nil, "", err
	}
	defer zr.Close()
	var entry *zip.File
	for _, f := range zr.File {
		if path.Clean(strings.ReplaceAll(f.Name, "\\", "/")) == images[id].Part {
			entry = f
			break
		}
	}
	if entry == nil {
		return nil, "", os.ErrNotExist
	}
	if entry.UncompressedSize64 > maxPictureBytes {
		return nil, "", fmt.Errorf("图片超过 %d MiB 安全限制", maxPictureBytes>>20)
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, "", err
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, maxPictureBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(content) > maxPictureBytes {
		return nil, "", errors.New("图片解压数据超限")
	}
	return content, path.Base(images[id].Part), nil
}

func (w *Workbook) PictureBytes(meta SheetMeta, id int) ([]byte, string, error) {
	content, _, err := w.PictureRaw(meta, id)
	if err != nil {
		return nil, "", err
	}
	mime := detectSafeImageType(content)
	if mime == "" {
		return nil, "", errors.New("图片类型暂不支持安全预览（支持 PNG、JPEG、GIF、WebP、BMP 和 SVG）")
	}
	return content, mime, nil
}

func detectSafeImageType(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff:
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	case bytes.HasPrefix(b, []byte("BM")):
		return "image/bmp"
	}
	// Browsers disable scripts inside an SVG <img>, but never trust that
	// boundary alone. Reject external references, active content and events.
	if len(b) > 0 && len(b) <= 8<<20 {
		source := strings.TrimSpace(string(b))
		if strings.HasPrefix(source, "<svg") || strings.HasPrefix(source, "<?xml") {
			decoder := xml.NewDecoder(strings.NewReader(source))
			seenRoot := false
			for {
				token, err := decoder.Token()
				if err == io.EOF && seenRoot {
					return "image/svg+xml"
				}
				if err != nil {
					break
				}
				switch tag := token.(type) {
				case xml.Directive:
					return "" // XML DTD / entity declarations
				case xml.StartElement:
					if !seenRoot && tag.Name.Local != "svg" {
						return ""
					}
					seenRoot = true
					blocked := map[string]bool{"script": true, "foreignObject": true, "iframe": true, "object": true, "embed": true, "audio": true, "video": true, "feImage": true, "image": true, "use": true, "animate": true, "set": true}
					if blocked[tag.Name.Local] {
						return ""
					}
					for _, attr := range tag.Attr {
						key := strings.ToLower(attr.Name.Local)
						value := strings.ToLower(strings.TrimSpace(attr.Value))
						if key == "xmlns" || attr.Name.Space == "xmlns" {
							if value != "http://www.w3.org/2000/svg" && value != "http://www.w3.org/1999/xlink" {
								return ""
							}
							continue
						}
						if strings.HasPrefix(key, "on") || key == "href" || key == "style" ||
							strings.Contains(value, "url(") || strings.Contains(value, "http:") || strings.Contains(value, "https:") ||
							strings.Contains(value, "javascript:") || strings.Contains(value, "data:") {
							return ""
						}
					}
				}
			}
		}
	}
	return ""
}
