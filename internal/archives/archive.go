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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

func (a *Archive) unchanged() error {
	st, err := os.Stat(a.Path)
	if err != nil {
		return err
	}
	if st.Size() != a.stat.Size() || !st.ModTime().Equal(a.stat.ModTime()) {
		return errors.New("压缩包已改变，请重新打开")
	}
	return nil
}

func (a *Archive) ensureCache(ctx context.Context, progress Progress) (string, error) {
	archiveCacheAccess.RLock()
	defer archiveCacheAccess.RUnlock()
	a.cacheMu.Lock()
	defer a.cacheMu.Unlock()
	if err := a.unchanged(); err != nil {
		return "", err
	}
	if a.cacheDir != "" {
		return a.cacheDir, nil
	}
	h := sha256.New()
	r := io.NewSectionReader(a.f, 0, a.stat.Size())
	buffer := make([]byte, 1<<20)
	var done int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := r.Read(buffer)
		if n > 0 {
			_, _ = h.Write(buffer[:n])
			done += int64(n)
			if progress != nil {
				progress(done, a.stat.Size(), "正在核验压缩包内容")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if err := a.unchanged(); err != nil {
		return "", err
	}
	dir := filepath.Join(workbook.CacheRoot(), "archive-"+hex.EncodeToString(h.Sum(nil)))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	a.releaseCache = pinDirectory(dir)
	a.cacheDir = dir
	return a.cacheDir, nil
}

var cacheShards = func() [32]chan struct{} {
	var shards [32]chan struct{}
	for i := range shards {
		shards[i] = make(chan struct{}, 1)
	}
	return shards
}()

type cachedMember struct {
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	SHA256   string `json:"sha256"`
}

func (a *Archive) cacheStream(ctx context.Context, key string, src io.Reader, limit, expected int64, crc uint32, progress Progress) (string, error) {
	dir, err := a.ensureCache(ctx, progress)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(key))
	gate := cacheShards[digest[0]%32]
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	target := filepath.Join(dir, hex.EncodeToString(digest[:16])+strings.ToLower(filepath.Ext(key)))
	if raw, e := os.ReadFile(target + ".json"); e == nil {
		var receipt cachedMember
		if json.Unmarshal(raw, &receipt) == nil {
			if st, e := os.Stat(target); e == nil && st.Mode().IsRegular() && st.Size() == receipt.Size && st.ModTime().UnixNano() == receipt.Modified && (expected < 0 || receipt.Size == expected) {
				if progress != nil {
					progress(st.Size(), st.Size(), "已复用压缩包成员缓存")
				}
				return target, nil
			}
		}
	}
	file, err := os.CreateTemp(dir, ".extract-*")
	if err != nil {
		return "", err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	defer file.Close()
	h := sha256.New()
	checksum := crc32.NewIEEE()
	count := int64(0)
	last := time.Now()
	buf := make([]byte, 1<<20)
	r := io.LimitReader(contextReader{ctx, src}, limit+1)
	for {
		n, e := r.Read(buf)
		if n > 0 {
			count += int64(n)
			if count > limit {
				return "", fmt.Errorf("解压数据超过 %dGB 上限", limit>>30)
			}
			if _, err = file.Write(buf[:n]); err != nil {
				return "", err
			}
			_, _ = h.Write(buf[:n])
			if crc != 0 {
				_, _ = checksum.Write(buf[:n])
			}
			if progress != nil && time.Since(last) > 80*time.Millisecond {
				progress(count, expected, "正在读取选中的压缩包成员")
				last = time.Now()
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", e
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if expected >= 0 && count != expected {
		return "", errors.New("压缩包成员长度不符，文件可能不完整")
	}
	if crc != 0 && checksum.Sum32() != crc {
		return "", errors.New("压缩包成员 CRC32 校验失败")
	}
	if err := a.unchanged(); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	st, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	receipt, _ := json.Marshal(cachedMember{Size: st.Size(), Modified: st.ModTime().UnixNano(), SHA256: hex.EncodeToString(h.Sum(nil))})
	if err := os.WriteFile(target+".json", receipt, 0600); err != nil {
		return "", err
	}
	if progress != nil {
		progress(count, count, "压缩包成员读取完成")
	}
	return target, nil
}

// Extract never opens unrelated ZIP/7z members. TAR members use indexed byte
// ranges; compressed TAR is expanded once into one seekable container cache.
func (a *Archive) Extract(ctx context.Context, id int, progress Progress) (string, Member, error) {
	if id < 0 || id >= len(a.Members) {
		return "", Member{}, errors.New("压缩包成员不存在")
	}
	m := a.Members[id]
	if m.Incomplete {
		return "", m, errors.New("该成员不完整；请在压缩包目录中选择查看 TXT 片段，或重新获取完整压缩包")
	}
	path, m, err := a.extractMember(ctx, m, progress)
	if err == nil {
		switch strings.ToLower(filepath.Ext(m.Name)) {
		case ".db", ".sqlite", ".sqlite3":
			err = a.attachSQLiteWAL(ctx, m, path, progress)
		}
	}
	return path, m, err
}

// ExtractFragment preserves every available byte, including an unfinished
// final line. Parsing a confirmed line-based TXT uses a separate view/cache.
func (a *Archive) ExtractFragment(ctx context.Context, id int, progress Progress) (string, Member, error) {
	if id < 0 || id >= len(a.Members) {
		return "", Member{}, errors.New("压缩包成员不存在")
	}
	m := a.Members[id]
	if !m.Incomplete || a.tarFile == nil || !strings.EqualFold(filepath.Ext(m.Name), ".txt") {
		return "", m, errors.New("仅支持查看不完整 TAR 成员的 TXT 片段")
	}
	return a.extractMember(ctx, m, progress)
}

func (a *Archive) extractMember(ctx context.Context, m Member, progress Progress) (string, Member, error) {
	if err := a.unchanged(); err != nil {
		return "", m, err
	}
	// Check the cache before creating a costly solid-stream decoder.
	key := fmt.Sprintf("member-%d-%s", m.index, m.Name)
	if m.Incomplete {
		key = "fragment-v1-" + key
	}
	if dir, err := a.ensureCache(ctx, progress); err != nil {
		return "", m, err
	} else {
		digest := sha256.Sum256([]byte(key))
		target := filepath.Join(dir, hex.EncodeToString(digest[:16])+strings.ToLower(filepath.Ext(key)))
		if raw, e := os.ReadFile(target + ".json"); e == nil {
			var receipt cachedMember
			if json.Unmarshal(raw, &receipt) == nil {
				if st, e := os.Stat(target); e == nil && st.Mode().IsRegular() && st.Size() == receipt.Size && st.ModTime().UnixNano() == receipt.Modified && (m.Size < 0 || st.Size() == m.Size) {
					if progress != nil {
						progress(st.Size(), st.Size(), "已复用压缩包成员缓存")
					}
					return target, m, nil
				}
			}
		}
	}
	var src io.Reader
	var closer io.Closer
	var err error
	if a.s != nil || a.Format == "RAR" {
		select {
		case decodeSlots <- struct{}{}:
			defer func() { <-decodeSlots }()
		case <-ctx.Done():
			return "", m, ctx.Err()
		}
	}
	switch {
	case a.z != nil:
		closer, err = a.z.File[m.index].Open()
		if err == nil {
			src = closer.(io.Reader)
		}
	case a.s != nil:
		if progress != nil {
			progress(0, m.Size, "正在定位 7z 数据流（固实压缩包需顺序解码）")
		}
		// The operation owns no extra OS file handles. Its decoder graph is
		// scoped to this call, and every compressed read (including solid seek)
		// observes cancellation. No decoder pool survives in the directory view.
		reader, e := sevenzip.NewReader(contextReaderAt{ctx, a.f}, a.stat.Size())
		if e != nil {
			return "", m, e
		}
		if m.index >= len(reader.File) {
			return "", m, errors.New("压缩包成员目录已改变")
		}
		closer, err = reader.File[m.index].Open()
		if err == nil {
			src = closer.(io.Reader)
		}
	case a.Format == "RAR":
		src, closer, err = a.openRARMember(ctx, m, progress)
	case a.tarFile != nil:
		src = io.NewSectionReader(a.tarFile, m.offset, m.Size)
	default:
		var closeFunc func() error
		src, closeFunc, _, _, err = a.compressedReader()
		closer = closeWrapper(closeFunc)
	}
	if err != nil {
		return "", m, err
	}
	if closer != nil {
		defer closer.Close()
	}
	path, err := a.cacheStream(ctx, key, src, MaxMemberBytes, m.Size, m.crc, progress)
	if a.Format == "RAR" {
		err = rarReadError(err)
	}
	return path, m, err
}

func (a *Archive) attachSQLiteWAL(ctx context.Context, member Member, path string, progress Progress) error {
	var matches []Member
	for _, wal := range a.walMembers {
		if strings.EqualFold(wal.Name, member.Name+"-wal") {
			matches = append(matches, wal)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	if len(matches) > 1 {
		return errors.New("压缩包包含重复 SQLite WAL 日志，无法确定一致的数据快照")
	}
	if matches[0].Incomplete {
		return errors.New("压缩包中的 SQLite WAL 日志不完整，无法读取一致的数据快照；请重新获取完整压缩包")
	}
	if matches[0].Size > MaxMemberBytes {
		return errors.New("SQLite WAL 日志超过 16GB 成员上限")
	}
	wal, _, err := a.extractMember(ctx, matches[0], progress)
	if err != nil {
		return fmt.Errorf("读取 SQLite WAL 日志失败：%w", err)
	}
	// SQLite needs the immutable WAL bytes beside the selected DB. It rebuilds
	// its own transient SHM; source archive/DB/WAL bytes remain read-only.
	target := path + "-wal"
	digest := sha256.Sum256([]byte(target))
	gate := cacheShards[digest[0]%32]
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if st, e := os.Stat(target); e == nil && st.Size() == matches[0].Size {
		return nil
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(wal, target); err == nil {
		return nil
	}
	source, err := os.Open(wal)
	if err != nil {
		return err
	}
	defer source.Close()
	tmp, err := os.CreateTemp(filepath.Dir(target), ".wal-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := io.CopyBuffer(tmp, contextReader{ctx, source}, make([]byte, 256<<10)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

type closeWrapper func() error

func (c closeWrapper) Close() error {
	if c != nil {
		return c()
	}
	return nil
}
