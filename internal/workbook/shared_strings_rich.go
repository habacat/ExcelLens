package workbook

import (
	"archive/zip"
	"encoding/binary"
	"encoding/xml"
	"path/filepath"
	"runtime"
)

type sharedStringPresentation struct {
	read    func(*xml.Decoder, xml.StartElement) (string, *RichText, error)
	observe func(int, string, *RichText)
	reset   func()
}

func (w *Workbook) RichTextNotice() string { return w.richNotice }
func (w *Workbook) sharedRichPath() string { return filepath.Join(w.CacheDir, "rich-shared-v1.bin") }

func (w *Workbook) readSharedStringsWithRich(part *zip.File) ([]string, error) {
	path := w.sharedRichPath()
	if cached, err := openRichTextStore(path, int(^uint(0)>>1)); err == nil {
		values, readErr := readSharedStrings(part)
		if readErr == nil && (cached.count == 0 || uint64(binary.LittleEndian.Uint32(cached.data[cached.indexStart+(cached.count-1)*richStoreIndexEntry:])) < uint64(len(values))) {
			w.richShared = cached
			return values, nil
		}
		runtime.SetFinalizer(cached, nil)
		cached.release()
		if readErr != nil {
			return nil, readErr
		}
	}
	builder := richTextStoreBuilder{dir: w.CacheDir}
	defer func() { builder.close() }()
	var metadataErr error
	presentation := &sharedStringPresentation{read: w.readRichTextStyled}
	presentation.observe = func(id int, text string, rich *RichText) {
		if metadataErr == nil {
			metadataErr = builder.append(id, text, rich)
		}
	}
	presentation.reset = func() { builder.close(); builder = richTextStoreBuilder{dir: w.CacheDir}; metadataErr = nil }
	values, err := readSharedStringsWithPresentation(part, presentation)
	if err != nil {
		return nil, err
	}
	if metadataErr == nil {
		w.richShared, metadataErr = builder.finish(path, len(values))
	}
	if metadataErr != nil {
		w.richNotice = "富文本样式未载入，原文保留：" + metadataErr.Error()
	}
	return values, nil
}
