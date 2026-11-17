package export

import (
	"archive/zip"
	"compress/flate"
	"context"
	"errors"
	"io"
	"strings"

	"excellens/internal/formats"
)

// WriteCSVArchive streams exactly one ordinary CSV without an intermediate
// uncompressed file. The caller owns atomic publication and the CSV schema.
// It is a standard ZIP: no custom decoder or secondary provenance file.
func WriteCSVArchive(ctx context.Context, dst io.Writer, name string, produce func(io.Writer) error) error {
	if dst == nil || produce == nil || !strings.HasSuffix(strings.ToLower(name), ".csv") {
		return errors.New("压缩CSV的输出或文件名无效")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name = safePartName(name[:len(name)-4]) + ".csv"
	zw := zip.NewWriter(archiveContextWriter{ctx, dst})
	// Fast DEFLATE was measured against levels 3/default for repeated complete
	// provenance rows. Compression remains local to this export, not a global
	// ZIP compressor change that could alter source/resume writers.
	zw.RegisterCompressor(zip.Deflate, func(dst io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(dst, flate.BestSpeed)
	})
	item, err := zw.Create(name)
	if err == nil {
		err = produce(archiveContextWriter{ctx, item})
	}
	closeErr := zw.Close()
	if err != nil {
		return errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return ctx.Err()
}

type archiveContextWriter struct {
	ctx context.Context
	out io.Writer
}

func (w archiveContextWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		if err := w.ctx.Err(); err != nil {
			return written, err
		}
		chunk := data[:min(len(data), formats.ExportArchiveChunkBytes)]
		n, err := w.out.Write(chunk)
		written += n
		if err != nil {
			return written, err
		}
		if n != len(chunk) {
			return written, io.ErrShortWrite
		}
		data = data[len(chunk):]
	}
	return written, w.ctx.Err()
}
