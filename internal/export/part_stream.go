package export

import (
	"encoding/binary"
	"errors"
	"io"

	"excellens/internal/formats"
)

// PartWriter frames an opaque section with bounded chunks and an explicit
// terminator. Framing is transport only; the journal acknowledges the entire
// business record/part, never an arbitrary XML or CSV byte boundary.
type PartWriter struct {
	out    io.Writer
	err    error
	closed bool
}

func NewPartWriter(out io.Writer) *PartWriter { return &PartWriter{out: out} }

func (w *PartWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, errors.New("导出部件已经关闭")
	}
	var header [4]byte
	written := 0
	for len(data) > 0 {
		count := min(len(data), formats.ExportPartChunkBytes)
		binary.LittleEndian.PutUint32(header[:], uint32(count))
		if w.err = writePartBytes(w.out, header[:]); w.err != nil {
			break
		}
		if w.err = writePartBytes(w.out, data[:count]); w.err != nil {
			break
		}
		written += count
		data = data[count:]
	}
	return written, w.err
}

func (w *PartWriter) Close() error {
	if w.closed || w.err != nil {
		return w.err
	}
	w.closed = true
	w.err = writePartBytes(w.out, make([]byte, 4))
	return w.err
}

func writePartBytes(out io.Writer, data []byte) error {
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

// PartReader consumes exactly one framed section, leaving the next section or
// final cursor untouched. Truncation and oversized frames are explicit errors.
type PartReader struct {
	in        io.Reader
	remaining uint32
	done      bool
	err       error
}

func NewPartReader(in io.Reader) *PartReader { return &PartReader{in: in} }

func (r *PartReader) Read(out []byte) (int, error) {
	if len(out) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.done {
		return 0, io.EOF
	}
	if r.remaining == 0 {
		var header [4]byte
		if _, err := io.ReadFull(r.in, header[:]); err != nil {
			r.err = io.ErrUnexpectedEOF
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				r.err = err
			}
			return 0, r.err
		}
		r.remaining = binary.LittleEndian.Uint32(header[:])
		if r.remaining > formats.ExportPartChunkBytes {
			r.err = errors.New("导出部件块长度无效")
			return 0, r.err
		}
		if r.remaining == 0 {
			r.done = true
			return 0, io.EOF
		}
	}
	n, err := r.in.Read(out[:min(len(out), int(r.remaining))])
	r.remaining -= uint32(n)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		r.err = err
	}
	return n, err
}
