package workbook

// Minimal, bounded Compound File Binary reader for BIFF workbooks. The input
// remains on disk; chains of sectors are indexed rather than copied into RAM.
import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
)

const (
	cfbFree = uint32(0xFFFFFFFF)
	cfbEnd  = uint32(0xFFFFFFFE)
	cfbFat  = uint32(0xFFFFFFFD)
	cfbDif  = uint32(0xFFFFFFFC)
)

type cfbReader struct {
	f        io.ReaderAt
	closer   io.Closer
	size     int64
	sector   int64
	fat      []uint32
	miniFat  []uint32
	root     *cfbStream
	workbook *cfbStream
}
type cfbStream struct {
	c     *cfbReader
	chain []uint32
	size  int64
	mini  bool
}

func (s *cfbStream) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= s.size {
		return 0, io.EOF
	}
	requested := len(p)
	if int64(requested) > s.size-off {
		p = p[:int(s.size-off)]
	}
	completed := 0
	step := s.c.sector
	if s.mini {
		step = 64
	}
	for len(p) > 0 {
		i := off / step
		if i >= int64(len(s.chain)) {
			return completed, io.ErrUnexpectedEOF
		}
		inside := off % step
		n := int(step - inside)
		if n > len(p) {
			n = len(p)
		}
		var e error
		if s.mini {
			if s.c.root == nil {
				return completed, errors.New("CFB 缺少 MiniStream 根目录")
			}
			_, e = s.c.root.ReadAt(p[:n], int64(s.chain[i])*64+inside)
		} else {
			// Most BIFF workbooks place sequential FAT sectors contiguously.
			// Coalesce runs so reading a 256 KiB BIFF block does not require
			// 512 tiny os.ReadAt calls for its 512-byte CFB sectors.
			run := 1
			maxRun := (int64(len(p)) + inside + step - 1) / step
			for int64(run) < maxRun && i+int64(run) < int64(len(s.chain)) &&
				s.chain[i+int64(run)] == s.chain[i]+uint32(run) {
				run++
			}
			available := int64(run)*step - inside
			if int64(len(p)) < available {
				available = int64(len(p))
			}
			n = int(available)
			pos := (int64(s.chain[i])+1)*s.c.sector + inside
			if pos < 0 || pos+int64(n) > s.c.size {
				return completed, errors.New("CFB 扇区越界")
			}
			_, e = s.c.f.ReadAt(p[:n], pos)
		}
		if e != nil {
			return completed, e
		}
		p = p[n:]
		off += int64(n)
		completed += n
	}
	if completed < requested {
		return completed, io.EOF
	}
	return completed, nil
}
func (c *cfbReader) Close() error {
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}

// EmbeddedWorkbook opens the OOXML Package stream offered by Excel on copy.
// The bounded FAT reader also handles fragmented and miniature streams.
func EmbeddedWorkbook(r io.ReaderAt, size int64) (*io.SectionReader, error) {
	c, err := parseCompound(r, size, func(name string) bool { return name == "package" })
	if err != nil {
		return nil, err
	}
	return io.NewSectionReader(c.workbook, 0, c.workbook.size), nil
}
func openCFB(path string) (*cfbReader, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	st, err := f.Stat()
	if err == nil {
		var c *cfbReader
		c, err = parseCompound(f, st.Size(), func(name string) bool { return name == "workbook" || name == "book" })
		if err == nil {
			c.closer = f
			return c, nil
		}
	}
	_ = f.Close()
	return nil, err
}

func parseCompound(r io.ReaderAt, size int64, match func(string) bool) (*cfbReader, error) {
	c := &cfbReader{f: r, size: size}
	var e error
	bad := func(e error) (*cfbReader, error) { return nil, e }
	var hdr [512]byte
	if _, e = r.ReadAt(hdr[:], 0); e != nil {
		return bad(fmt.Errorf("不是有效的 OLE2/CFB 文件（文件头不完整）：%w", e))
	}
	if string(hdr[:8]) != "\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1" {
		return bad(errors.New("不是有效的 OLE2/CFB XLS 二进制文件"))
	}
	major := binary.LittleEndian.Uint16(hdr[26:28])
	shift := binary.LittleEndian.Uint16(hdr[30:32])
	if (major != 3 || shift != 9) && (major != 4 || shift != 12) {
		return bad(errors.New("CFB 文件版本与扇区大小不匹配"))
	}
	c.sector = 1 << shift
	if c.size < c.sector {
		return bad(errors.New("CFB 文件不足一个扇区"))
	}
	capacity := uint32((c.size - c.sector) / c.sector)
	if capacity > 1<<25 {
		return bad(errors.New("CFB 扇区数超过安全上限"))
	}
	fatCount := binary.LittleEndian.Uint32(hdr[44:48])
	dirFirst := binary.LittleEndian.Uint32(hdr[48:52])
	miniCutoff := binary.LittleEndian.Uint32(hdr[56:60])
	miniFirst := binary.LittleEndian.Uint32(hdr[60:64])
	miniCount := binary.LittleEndian.Uint32(hdr[64:68])
	difFirst := binary.LittleEndian.Uint32(hdr[68:72])
	difCount := binary.LittleEndian.Uint32(hdr[72:76])
	if fatCount > capacity || difCount > capacity || miniCount > capacity {
		return bad(errors.New("CFB 分配表数量无效"))
	}
	readSector := func(id uint32) ([]byte, error) {
		if id >= capacity {
			return nil, errors.New("CFB 扇区索引越界")
		}
		b := make([]byte, c.sector)
		_, e := r.ReadAt(b, (int64(id)+1)*c.sector)
		return b, e
	}
	fatIDs := make([]uint32, 0, fatCount)
	for i := 76; i < 512 && len(fatIDs) < int(fatCount); i += 4 {
		sid := binary.LittleEndian.Uint32(hdr[i : i+4])
		if sid != cfbFree {
			fatIDs = append(fatIDs, sid)
		}
	}
	next := difFirst
	seenDif := map[uint32]struct{}{}
	for i := uint32(0); i < difCount && len(fatIDs) < int(fatCount); i++ {
		if next == cfbEnd || next == cfbFree {
			return bad(errors.New("CFB DIFAT 链过短"))
		}
		if _, ok := seenDif[next]; ok {
			return bad(errors.New("CFB DIFAT 链成环"))
		}
		seenDif[next] = struct{}{}
		b, e := readSector(next)
		if e != nil {
			return bad(e)
		}
		for j := 0; j+4 < len(b) && len(fatIDs) < int(fatCount); j += 4 {
			sid := binary.LittleEndian.Uint32(b[j : j+4])
			if sid != cfbFree {
				fatIDs = append(fatIDs, sid)
			}
		}
		next = binary.LittleEndian.Uint32(b[len(b)-4:])
	}
	if uint32(len(fatIDs)) != fatCount {
		return bad(errors.New("CFB FAT 链不完整"))
	}
	c.fat = make([]uint32, 0, int(fatCount)*(int(c.sector)/4))
	for _, sid := range fatIDs {
		b, e := readSector(sid)
		if e != nil {
			return bad(e)
		}
		for i := 0; i < len(b); i += 4 {
			c.fat = append(c.fat, binary.LittleEndian.Uint32(b[i:i+4]))
		}
	}
	chain := func(first uint32, table []uint32, max int) ([]uint32, error) {
		var result []uint32
		visited := make(map[uint32]struct{})
		for id := first; id != cfbEnd; {
			if id >= uint32(len(table)) || int(id) >= max {
				return nil, errors.New("CFB FAT 链扇区越界")
			}
			if _, ok := visited[id]; ok {
				return nil, errors.New("CFB FAT 链成环")
			}
			visited[id] = struct{}{}
			result = append(result, id)
			if len(result) > max {
				return nil, errors.New("CFB FAT 链过长")
			}
			id = table[id]
		}
		return result, nil
	}
	dirs, e := chain(dirFirst, c.fat, int(capacity))
	if e != nil {
		return bad(e)
	}
	if len(dirs) == 0 {
		return bad(errors.New("CFB 缺少目录扇区"))
	}
	rootDir := &cfbStream{c: c, chain: dirs, size: int64(len(dirs)) * c.sector}
	// Only directory metadata (not workbook data) is materialized.
	var rootFirst = cfbEnd
	var rootSize int64
	var bookFirst = cfbEnd
	var bookSize int64
	for offset := int64(0); offset+128 <= rootDir.size; offset += 128 {
		var item [128]byte
		if _, e := rootDir.ReadAt(item[:], offset); e != nil {
			return bad(e)
		}
		n := int(binary.LittleEndian.Uint16(item[64:66]))
		if n < 2 || n > 64 || n%2 != 0 {
			continue
		}
		u := make([]uint16, 0, n/2)
		for i := 0; i < n-2; i += 2 {
			u = append(u, binary.LittleEndian.Uint16(item[i:i+2]))
		}
		name := strings.ToLower(string(utf16.Decode(u)))
		first := binary.LittleEndian.Uint32(item[116:120])
		// CFB v3 directory stream sizes are 32-bit. Some legacy
		// producers left the unused high DWORD uninitialized.
		var sz int64
		if major == 3 {
			sz = int64(binary.LittleEndian.Uint32(item[120:124]))
		} else {
			sz = int64(binary.LittleEndian.Uint64(item[120:128]))
		}
		if sz < 0 || sz > c.size {
			return bad(errors.New("CFB 流长度不合理"))
		}
		switch {
		case item[66] == 5:
			rootFirst = first
			rootSize = sz
		case item[66] == 2 && match(name):
			bookFirst = first
			bookSize = sz
		}
	}
	if bookFirst == cfbEnd {
		return bad(errors.New("文件内找不到 Workbook/Book 数据流"))
	}
	if rootFirst != cfbEnd && rootSize > 0 {
		rootChain, e := chain(rootFirst, c.fat, int(capacity))
		if e != nil {
			return bad(e)
		}
		if int64(len(rootChain))*c.sector < rootSize {
			return bad(errors.New("CFB MiniStream 根数据不足"))
		}
		c.root = &cfbStream{c: c, chain: rootChain, size: rootSize}
	}
	if miniCount > 0 {
		miniChain, e := chain(miniFirst, c.fat, int(capacity))
		if e != nil {
			return bad(e)
		}
		if len(miniChain) < int(miniCount) {
			return bad(errors.New("CFB MiniFAT 链不完整"))
		}
		c.miniFat = make([]uint32, 0, int(miniCount)*int(c.sector)/4)
		for i := uint32(0); i < miniCount; i++ {
			b, e := readSector(miniChain[i])
			if e != nil {
				return bad(e)
			}
			for j := 0; j < len(b); j += 4 {
				c.miniFat = append(c.miniFat, binary.LittleEndian.Uint32(b[j:j+4]))
			}
		}
	}
	mini := bookSize < int64(miniCutoff)
	table := c.fat
	max := int(capacity)
	if mini {
		table = c.miniFat
		max = len(table)
		if c.root == nil {
			return bad(errors.New("CFB 小数据流缺少根 MiniStream"))
		}
	}
	bookChain, e := chain(bookFirst, table, max)
	if e != nil {
		return bad(e)
	}
	blockSize := c.sector
	if mini {
		blockSize = 64
	}
	if int64(len(bookChain))*blockSize < bookSize {
		return bad(fmt.Errorf("CFB Workbook 流截断（%d 字节）", bookSize))
	}
	c.workbook = &cfbStream{c: c, chain: bookChain, size: bookSize, mini: mini}
	return c, nil
}
