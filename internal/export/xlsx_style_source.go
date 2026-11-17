package export

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type styleContextReader struct {
	ctx    context.Context
	reader io.Reader
}

// Bound each write so cancellation can interrupt a large unchanged XML span.
// Writers returning short success must never yield a seemingly complete ZIP.
type styleContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w styleContextWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		if err := w.ctx.Err(); err != nil {
			return total, err
		}
		size := min(len(p), 32<<10)
		n, err := w.writer.Write(p[:size])
		if n < 0 || n > size {
			return total, io.ErrShortWrite
		}
		total += n
		if err != nil {
			return total, err
		}
		if n != size {
			return total, io.ErrShortWrite
		}
		p = p[size:]
	}
	return total, w.ctx.Err()
}

func (w styleContextWriter) WriteString(s string) (int, error) {
	total := 0
	for len(s) > 0 {
		if err := w.ctx.Err(); err != nil {
			return total, err
		}
		size := min(len(s), 32<<10)
		n, err := io.WriteString(w.writer, s[:size])
		if n < 0 || n > size {
			return total, io.ErrShortWrite
		}
		total += n
		if err != nil {
			return total, err
		}
		if n != size {
			return total, io.ErrShortWrite
		}
		s = s[size:]
	}
	return total, w.ctx.Err()
}

func (r styleContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Capture both parts before writing cells. Cached values can outlive a live
// file change, but must never be combined with a different source stylesheet.
func loadXLSXStyleSource(ctx context.Context, wb *workbook.Workbook) (style string, theme []byte, err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if err := wb.CheckSourceUnchanged(); err != nil {
		return "", nil, err
	}
	zr, err := zip.OpenReader(wb.Path)
	if err != nil {
		return "", nil, err
	}
	defer zr.Close()
	seen := make(map[string]bool, 2)
	for _, entry := range zr.File {
		name := path.Clean(strings.ReplaceAll(entry.Name, "\\", "/"))
		if name != "xl/styles.xml" && name != "xl/theme/theme1.xml" {
			continue
		}
		if seen[name] {
			return "", nil, errors.New("源样式或主题部件重名，无法完整导出")
		}
		seen[name] = true
		if entry.UncompressedSize64 > formats.XLSXStylePartBytes {
			return "", nil, fmt.Errorf("样式/主题部件超过64MiB：%s", name)
		}
		r, err := entry.Open()
		if err != nil {
			return "", nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(styleContextReader{ctx, r}, formats.XLSXStylePartBytes+1))
		closeErr := r.Close()
		if readErr != nil {
			return "", nil, readErr
		}
		if closeErr != nil {
			return "", nil, closeErr
		}
		if len(data) > formats.XLSXStylePartBytes {
			return "", nil, errors.New("样式/主题实际内容超过读取容量")
		}
		if name == "xl/styles.xml" {
			style = string(data)
		} else {
			theme = data
		}
	}
	if err := wb.CheckSourceUnchanged(); err != nil {
		return "", nil, err
	}
	return style, theme, ctx.Err()
}
