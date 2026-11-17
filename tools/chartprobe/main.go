// chartprobe independently checks generated chart packages with Go's standard
// ZIP/CRC, PNG and XML readers. It is a developer tool, never a runtime helper.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

type evidence struct {
	File, Kind, Mode, ImageSHA256, JSONSHA256 string
	Width, Height, SVGNodes, Bytes            int
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func verify(file string) (evidence, error) {
	r, err := zip.OpenReader(file)
	if err != nil {
		return evidence{}, err
	}
	defer r.Close()
	if len(r.File) != 3 {
		return evidence{}, errors.New("expected image, exact JSON and README")
	}
	parts := make(map[string][]byte)
	var size uint64
	for _, f := range r.File {
		size += f.UncompressedSize64
		if size > 40<<20 || f.Method != zip.Store || parts[f.Name] != nil {
			return evidence{}, errors.New("invalid package budget or entries")
		}
		stream, err := f.Open()
		if err != nil {
			return evidence{}, err
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, 40<<20+1))
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil || uint64(len(data)) != f.UncompressedSize64 {
			return evidence{}, errors.Join(readErr, closeErr, errors.New("entry size/CRC failed"))
		}
		parts[f.Name] = data
	}
	var data struct {
		Schema int    `json:"schema"`
		Kind   string `json:"kind"`
		Chart  struct {
			Mode string `json:"mode"`
		} `json:"chart"`
		Output struct {
			Type                                   string `json:"type"`
			Width, Height, PixelWidth, PixelHeight int
		} `json:"output"`
	}
	if err := json.Unmarshal(parts["exact-data.json"], &data); err != nil || data.Schema != 1 || len(parts["README.txt"]) == 0 {
		return evidence{}, errors.New("invalid exact-data envelope")
	}
	image := parts["chart."+data.Output.Type]
	if len(image) == 0 || len(parts) != 3 {
		return evidence{}, errors.New("missing declared image")
	}
	e := evidence{File: filepath.Base(file), Kind: data.Kind, Mode: data.Chart.Mode, ImageSHA256: digest(image), JSONSHA256: digest(parts["exact-data.json"]), Bytes: int(size)}
	switch data.Output.Type {
	case "png":
		img, err := png.Decode(bytes.NewReader(image))
		if err != nil {
			return evidence{}, err
		}
		e.Width, e.Height = img.Bounds().Dx(), img.Bounds().Dy()
		if e.Width != data.Output.PixelWidth || e.Height != data.Output.PixelHeight || int64(e.Width)*int64(e.Height) > 20_000_000 {
			return evidence{}, errors.New("PNG dimensions differ from attachment")
		}
	case "svg":
		decoder := xml.NewDecoder(bytes.NewReader(image))
		allowed := map[string]bool{"svg": true, "title": true, "desc": true, "defs": true, "clipPath": true, "g": true, "rect": true, "text": true, "path": true}
		attributes := map[string]bool{"xmlns": true, "width": true, "height": true, "viewBox": true, "role": true, "aria-labelledby": true, "id": true, "clip-path": true, "x": true, "y": true, "fill": true, "font-family": true, "font-size": true, "font-weight": true, "text-anchor": true, "space": true, "textLength": true, "lengthAdjust": true, "d": true, "stroke": true, "stroke-width": true}
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return evidence{}, err
			}
			switch t := token.(type) {
			case xml.Directive:
				return evidence{}, errors.New("SVG directive is forbidden")
			case xml.StartElement:
				e.SVGNodes++
				if !allowed[t.Name.Local] || t.Name.Space != "http://www.w3.org/2000/svg" {
					return evidence{}, errors.New("unexpected SVG element")
				}
				for _, a := range t.Attr {
					if !attributes[a.Name.Local] {
						return evidence{}, fmt.Errorf("unexpected SVG attribute %s", a.Name.Local)
					}
					if t.Name.Local == "svg" && a.Name.Local == "width" {
						e.Width, _ = strconv.Atoi(a.Value)
					}
					if t.Name.Local == "svg" && a.Name.Local == "height" {
						e.Height, _ = strconv.Atoi(a.Value)
					}
				}
			}
		}
		if e.Width != data.Output.Width || e.Height != data.Output.Height || e.SVGNodes < 10 {
			return evidence{}, errors.New("SVG dimensions or elements differ from attachment")
		}
	default:
		return evidence{}, errors.New("unknown image encoding")
	}
	return e, nil
}

func main() {
	dir := flag.String("dir", "", "directory containing generated ZIP packages")
	report := flag.String("report", "", "JSON receipt path")
	flag.Parse()
	files, err := filepath.Glob(filepath.Join(*dir, "*.zip"))
	if err != nil || *dir == "" || len(files) == 0 {
		panic("provide a nonempty generated package directory")
	}
	var cases []evidence
	for _, file := range files {
		e, err := verify(file)
		if err != nil {
			panic(fmt.Errorf("%s: %w", filepath.Base(file), err))
		}
		cases = append(cases, e)
	}
	data, _ := json.MarshalIndent(map[string]any{"result": "PASS", "cases": cases, "standardReaders": []string{"archive/zip CRC", "image/png full decode", "encoding/xml strict tokens"}, "desktopManualAcceptance": false}, "", "  ")
	if *report != "" {
		if err := os.WriteFile(*report, append(data, '\n'), 0600); err != nil {
			panic(err)
		}
	}
	fmt.Println(string(data))
}
