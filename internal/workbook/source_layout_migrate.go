package workbook

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
)

func readOOXMLSourceLayout(idx *SheetIndex, digits ...float64) (SourceLayout, error) {
	return readOOXMLSourceLayoutContext(context.Background(), idx, digits...)
}

// Older metadata is rebuilt once from the cached XML token stream, including
// layout-only rows omitted from the content index. Cell values are skipped as
// bounded byte spans, not decoded or retained. Warm reads use the versioned sidecar.
func readOOXMLSourceLayoutContext(ctx context.Context, idx *SheetIndex, digits ...float64) (SourceLayout, error) {
	layout := SourceLayout{DefaultRow: 20, DefaultCol: 64}
	if len(digits) > 0 {
		layout.digitWidth = digits[0]
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return layout, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return layout, err
	}
	if stat.Size() < 0 || stat.Size() > maxSheetXMLSize {
		return layout, fmt.Errorf("布局XML缓存大小无效")
	}
	counted := &countingReader{r: f}
	r := bufio.NewReaderSize(counted, 1<<20)
	var buffer []byte
	physicalRows := 0
	lastCheck := int64(-1)
	for {
		chunk, readErr := r.ReadSlice('<')
		current := counted.n - int64(r.Buffered())
		if current-lastCheck >= 1<<20 || lastCheck < 0 {
			if err := ctx.Err(); err != nil {
				return layout, err
			}
			lastCheck = current
		}
		if len(chunk) > 0 && chunk[len(chunk)-1] == '<' {
			start := current - 1
			tag, err := readXMLTag(r, buffer)
			if err != nil {
				return layout, err
			}
			buffer = tag
			info := inspectXMLTag(tag)
			row := 0
			if info.valid && !info.closing && bytes.Equal(info.name, []byte("row")) {
				physicalRows++
				row = parsePositiveIntBytes(xmlAttribute(tag, "r"))
				if row <= 0 {
					row = physicalRows
				}
			}
			layout.captureMetadataToken(tag, info, start, counted.n-int64(r.Buffered()), row)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != bufio.ErrBufferFull {
			return layout, readErr
		}
	}
	layout.finishMetadataCapture()
	return layout, ctx.Err()
}
