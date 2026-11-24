// Package searchresults stores full search hits on disk and reads bounded pages.
package searchresults

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode"

	"excellens/internal/formats"
	"excellens/internal/separated"
)

type Hit struct {
	Sheet   string `json:"sheet,omitempty"`
	Ref     string `json:"ref"`
	Row     int    `json:"row"`
	Col     int    `json:"col"`
	Formula string `json:"formula,omitempty"`
	Value   string `json:"value,omitempty"`
}
type Store struct {
	mu             sync.RWMutex
	writer         *bufio.Writer
	file           io.ReaderAt
	cleanup        func() error
	size, count    int64
	sealed, closed bool
}

func New(w io.Writer, file io.ReaderAt, cleanup func() error) *Store {
	return &Store{writer: bufio.NewWriterSize(w, 64<<10), file: file, cleanup: cleanup}
}

// A single producer appends and checkpoints until Seal. The manager publishes
// only sealed results; Close waits for readers which already borrowed a stream.
func (s *Store) Append(hit Hit) error {
	b, err := json.Marshal(hit)
	if err != nil {
		return err
	}
	if len(b) > formats.SearchRecordBytes {
		return errors.New("单项搜索命中超过记录容量")
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(b)))
	if _, err = s.writer.Write(header[:]); err == nil {
		_, err = s.writer.Write(b)
	}
	if err == nil {
		s.size += int64(len(b) + 4)
		s.count++
	}
	return err
}
func (s *Store) Count() int64 { return s.count }
func (s *Store) Size() int64  { return s.size }
func (s *Store) Range(start int64) (*io.SectionReader, error) {
	if err := s.writer.Flush(); err != nil {
		return nil, err
	}
	if start < 0 || start > s.size {
		return nil, errors.New("搜索结果游标无效")
	}
	return io.NewSectionReader(s.file, start, s.size-start), nil
}
func (s *Store) Seal() error {
	if err := s.writer.Flush(); err != nil {
		return err
	}
	s.sealed = true
	return nil
}
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.cleanup()
}
func Walk(ctx context.Context, r io.Reader, bytes int64, visit func(int64, Hit) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var head [4]byte
	var data []byte
	for pos := int64(0); pos < bytes; {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.ReadFull(br, head[:]); err != nil {
			return err
		}
		n := int64(binary.LittleEndian.Uint32(head[:]))
		if n <= 0 || n > formats.SearchRecordBytes || n > bytes-pos-4 {
			return errors.New("搜索结果记录损坏")
		}
		if int64(cap(data)) < n {
			data = make([]byte, n)
		}
		if _, err := io.ReadFull(br, data[:n]); err != nil {
			return err
		}
		var hit Hit
		if err := json.Unmarshal(data[:n], &hit); err != nil {
			return err
		}
		if err := visit(pos, hit); err != nil {
			return err
		}
		pos += n + 4
	}
	return nil
}

type Filter struct{ Text, Sheet string }

func (s *Store) visit(ctx context.Context, start int64, f Filter, visit func(int64, Hit) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed || !s.sealed {
		return errors.New("搜索结果尚未就绪或已释放")
	}
	if start < 0 || start > s.size {
		return errors.New("结果游标越界")
	}
	needle := strings.ToLower(strings.TrimSpace(f.Text))
	return Walk(ctx, io.NewSectionReader(s.file, start, s.size-start), s.size-start, func(pos int64, h Hit) error {
		if f.Sheet != "" && f.Sheet != h.Sheet {
			return nil
		}
		if needle != "" {
			found := false
			for _, v := range []string{h.Sheet, h.Ref, h.Formula, h.Value} {
				if strings.Contains(strings.ToLower(v), needle) {
					found = true
					break
				}
			}
			if !found {
				return nil
			}
		}
		return visit(start+pos, h)
	})
}

type Page struct {
	Matches        []Hit `json:"matches"`
	Next           int64 `json:"next"`
	More           bool  `json:"more"`
	PreviewClipped bool  `json:"previewClipped"`
}

func preview(value string) (string, bool) {
	count := 0
	for i := range value {
		if count == formats.SearchPreviewRunes {
			return value[:i] + "…", true
		}
		count++
	}
	return value, false
}
func (s *Store) Page(ctx context.Context, start int64, f Filter) (Page, error) {
	page := Page{Matches: []Hit{}}
	stop := errors.New("page complete")
	err := s.visit(ctx, start, f, func(pos int64, h Hit) error {
		if len(page.Matches) == formats.SearchPageRows {
			page.Next = pos
			page.More = true
			return stop
		}
		var a, b bool
		h.Value, a = preview(h.Value)
		h.Formula, b = preview(h.Formula)
		page.PreviewClipped = page.PreviewClipped || a || b
		page.Matches = append(page.Matches, h)
		return nil
	})
	if err == stop {
		err = nil
	}
	return page, err
}
func (s *Store) CSV(ctx context.Context, w io.Writer, f Filter) error {
	out := bufio.NewWriterSize(w, 64<<10)
	if _, err := io.WriteString(out, "\ufeff工作表,单元格,缓存显示值,原始公式\r\n"); err != nil {
		return err
	}
	err := s.visit(ctx, 0, f, func(_ int64, h Hit) error {
		for i, v := range []string{h.Sheet, h.Ref, h.Value, h.Formula} {
			if i > 0 {
				if err := out.WriteByte(','); err != nil {
					return err
				}
			}
			left := strings.TrimLeftFunc(v, unicode.IsSpace)
			if len(left) > 0 && strings.ContainsAny(left[:1], "=+-@") {
				v = "'" + v
			}
			if err := separated.WriteField(out, v, ',', true); err != nil {
				return err
			}
		}
		_, err := out.WriteString("\r\n")
		return err
	})
	if err != nil {
		return err
	}
	return out.Flush()
}
