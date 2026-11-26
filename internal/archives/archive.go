// Package archives provides lazy, read-only access to compressed data files.
// Member names never become filesystem paths: only the selected member is
// materialized, under a content-addressed cache with atomic publication.
package archives

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"excellens/internal/formats"
	"excellens/internal/workbook"
	"github.com/bodgit/sevenzip"
	"github.com/ulikunitz/xz"
	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	MaxEntries              = 200000
	MaxMemberBytes    int64 = 16 << 30
	MaxContainerBytes int64 = 64 << 30
)

type Member struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Packed       uint64 `json:"packed,omitempty"`
	Archive      bool   `json:"archive,omitempty"`
	Incomplete   bool   `json:"incomplete,omitempty"`
	DeclaredSize int64  `json:"declaredSize,omitempty"`
	offset       int64
	index        int
	crc          uint32
}

type Progress func(done, total int64, stage string)

type Archive struct {
	walMembers                  []Member
	Path, Name, Format          string
	Notice                      string
	Members                     []Member
	Total                       int
	f                           *os.File
	stat                        os.FileInfo
	z                           *zip.Reader
	s                           *sevenzip.ReadCloser
	tarFile                     *os.File
	cacheMu                     sync.Mutex
	cacheDir                    string
	releaseSource, releaseCache func()
	closeOnce                   sync.Once
	closeErr                    error
}

func SupportedExtension(ext string) bool {
	return formats.ArchiveExtension(ext)
}

func IsPath(name string) bool { return SupportedExtension(filepath.Ext(name)) }

func (a *Archive) Close() error {
	a.closeOnce.Do(func() {
		var errs []error
		if a.s != nil {
			errs = append(errs, a.s.Close())
		}
		if a.tarFile != nil && a.tarFile != a.f {
			errs = append(errs, a.tarFile.Close())
		}
		if a.f != nil {
			errs = append(errs, a.f.Close())
		}
		if a.releaseSource != nil {
			a.releaseSource()
		}
		if a.releaseCache != nil {
			a.releaseCache()
		}
		a.closeErr = errors.Join(errs...)
	})
	return a.closeErr
}

func (a *Archive) CacheDir() string {
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	return a.cacheDir
}

func Open(ctx context.Context, path, displayName string) (*Archive, error) {
	releaseSource := pinSource(path)
	f, err := os.Open(path)
	if err != nil {
		releaseSource()
		return nil, err
	}
	a := &Archive{Path: path, Name: displayName, f: f, releaseSource: releaseSource}
	if a.Name == "" {
		a.Name = filepath.Base(path)
	}
	bad := func(err error) (*Archive, error) { _ = a.Close(); return nil, err }
	a.stat, err = f.Stat()
	if err != nil {
		return bad(err)
	}
	if !a.stat.Mode().IsRegular() {
		return bad(errors.New("压缩包路径不是普通文件"))
	}
	ext := strings.ToLower(filepath.Ext(a.Name))
	switch ext {
	case ".zip":
		a.Format = "ZIP"
		a.z, err = zip.NewReader(f, a.stat.Size())
		if err != nil {
			return bad(err)
		}
		if len(a.z.File) > MaxEntries {
			return bad(errors.New("压缩包成员数量超过 200,000"))
		}
		for i, entry := range a.z.File {
			if err := ctx.Err(); err != nil {
				return bad(err)
			}
			name := entry.Name
			if entry.NonUTF8 && !utf8.ValidString(name) {
				if decoded, e := simplifiedchinese.GB18030.NewDecoder().String(name); e == nil {
					name = decoded
				}
			}
			a.add(name, int64(min(entry.UncompressedSize64, uint64(MaxMemberBytes)+1)), entry.CompressedSize64, i, 0, entry.CRC32, entry.Mode().IsRegular())
		}
	case ".7z":
		a.Format = "7Z"
		a.s, err = sevenzip.OpenReader(path)
		if err != nil {
			return bad(err)
		}
		if len(a.s.File) > MaxEntries {
			return bad(errors.New("压缩包成员数量超过 200,000"))
		}
		for i, entry := range a.s.File {
			a.add(entry.Name, int64(min(entry.UncompressedSize, uint64(MaxMemberBytes)+1)), 0, i, 0, entry.CRC32, entry.FileInfo().Mode().IsRegular())
		}
	case ".rar":
		a.Format = "RAR"
		err = a.listRAR(ctx)
	case ".tar":
		a.Format, a.tarFile = "TAR", f
		err = a.listTar(ctx)
	default:
		source, closeSource, format, name, e := a.compressedReader()
		if e != nil {
			return bad(e)
		}
		defer closeSource()
		a.Format = format
		br := bufio.NewReaderSize(source, 64<<10)
		prefix, _ := br.Peek(512)
		_, tarErr := tar.NewReader(strings.NewReader(string(prefix))).Next()
		lower := strings.ToLower(a.Name)
		isTar := tarErr == nil || strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tar.bz2") || strings.HasSuffix(lower, ".tar.xz") || ext == ".tgz" || ext == ".tbz" || ext == ".tbz2" || ext == ".txz"
		if isTar {
			a.Format = "TAR." + format
			container, e := a.cacheStream(ctx, "container.tar", br, MaxContainerBytes, -1, 0, nil)
			if e != nil {
				return bad(e)
			}
			a.tarFile, err = os.Open(container)
			if err == nil {
				err = a.listTar(ctx)
			}
		} else {
			if name == "" {
				name = strings.TrimSuffix(a.Name, filepath.Ext(a.Name))
			}
			if !workbook.SupportedDataExtension(filepath.Ext(name)) && !IsPath(name) {
				name += ".txt"
			}
			a.add(name, -1, uint64(a.stat.Size()), 0, 0, 0, true)
		}
	}
	if err != nil {
		return bad(err)
	}
	if len(a.Members) == 0 {
		if a.Notice != "" {
			return bad(errors.New(a.Notice + " 没有可安全读取的成员，请重新获取完整压缩包。"))
		}
		return bad(errors.New("压缩包中没有可打开的 Excel、文本、SQLite 或压缩包文件（单成员上限 16GB）"))
	}
	return a, nil
}

func (a *Archive) add(name string, size int64, packed uint64, index int, offset int64, crc uint32, regular bool) {
	a.Total++
	if !regular || strings.ContainsRune(name, 0) || len(name) > 8192 {
		return
	}
	if strings.HasSuffix(strings.ToLower(name), "-wal") {
		a.walMembers = append(a.walMembers, Member{Name: strings.ReplaceAll(name, "\\", "/"), Size: size, index: index, offset: offset, crc: crc})
	}
	if size > MaxMemberBytes {
		return
	}
	if !workbook.SupportedDataExtension(filepath.Ext(name)) && !IsPath(name) {
		return
	}
	a.Members = append(a.Members, Member{ID: len(a.Members), Name: strings.ReplaceAll(name, "\\", "/"), Size: size, Packed: packed, Archive: IsPath(name), offset: offset, index: index, crc: crc})
}

type tarPosition struct {
	f   *os.File
	pos int64
}

func (r *tarPosition) Read(p []byte) (int, error) {
	n, e := r.f.Read(p)
	r.pos += int64(n)
	return n, e
}

func (r *tarPosition) Seek(off int64, whence int) (int64, error) {
	n, e := r.f.Seek(off, whence)
	r.pos = n
	return n, e
}

func (a *Archive) listTar(ctx context.Context) error {
	st, err := a.tarFile.Stat()
	if err != nil {
		return err
	}
	if _, err := a.tarFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	position := &tarPosition{f: a.tarFile}
	r := tar.NewReader(position)
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				a.Notice = "TAR 文件尾部的目录或填充不完整；仅列出此前确认完整的成员，后续文件可能缺失。请重新获取完整压缩包。"
				return nil
			}
			return fmt.Errorf("读取 TAR 目录失败（字节位置 %d）：%w", position.pos, err)
		}
		if i >= MaxEntries {
			return errors.New("压缩包成员数量超过 200,000")
		}
		regular := header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA
		// Sparse logical sizes are not contiguous byte ranges. Never expose
		// those bytes as ordinary files or diagnose them from logical size.
		for key := range header.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse.") {
				regular = false
			}
		}
		available := max(int64(0), st.Size()-position.pos)
		if regular && header.Size > available {
			a.Notice = fmt.Sprintf("TAR 文件不完整：成员 %q 声明 %d 字节，实际仅存 %d 字节，缺少 %d 字节；后续文件可能缺失。请重新获取完整压缩包。", header.Name, header.Size, available, header.Size-available)
			before := len(a.Members)
			// Only a TXT fragment can be offered for deliberate recovery. XLS,
			// databases and nested archives must still pass their full lengths.
			canRecover := strings.EqualFold(filepath.Ext(header.Name), ".txt") && available > 0 && header.Size <= MaxMemberBytes
			if strings.HasSuffix(strings.ToLower(header.Name), "-wal") {
				a.walMembers = append(a.walMembers, Member{Name: strings.ReplaceAll(header.Name, "\\", "/"), Size: available, index: i, offset: position.pos, Incomplete: true, DeclaredSize: header.Size})
			}
			a.add(header.Name, available, 0, i, position.pos, 0, canRecover)
			if len(a.Members) > before {
				a.Members[before].Incomplete = true
				a.Members[before].DeclaredSize = header.Size
			}
			return nil
		}
		a.add(header.Name, header.Size, 0, i, position.pos, 0, regular)
	}
}

func (a *Archive) compressedReader() (io.Reader, func() error, string, string, error) {
	section := io.NewSectionReader(a.f, 0, a.stat.Size())
	nop := func() error { return nil }
	switch strings.ToLower(filepath.Ext(a.Name)) {
	case ".gz", ".tgz":
		r, err := gzip.NewReader(section)
		if err != nil {
			return nil, nop, "", "", err
		}
		return r, r.Close, "GZ", r.Name, nil
	case ".bz2", ".tbz", ".tbz2":
		return bzip2.NewReader(section), nop, "BZ2", "", nil
	case ".xz", ".txz":
		r, err := xz.NewReader(section)
		return r, nop, "XZ", "", err
	}
	return nil, nop, "", "", errors.New("不支持的压缩格式")
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

type contextReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.ReadAt(p, off)
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type closeWrapper func() error

func (c closeWrapper) Close() error {
	if c != nil {
		return c()
	}
	return nil
}
