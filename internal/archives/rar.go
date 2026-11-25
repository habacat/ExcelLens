package archives

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"excellens/internal/formats"
	"github.com/nwaples/rardecode/v2"
)

const rarSourceName = "archive.rar"
const rarBufferBytes = 64 << 10

// The decoder can ask its filesystem for another volume. Keep every operation
// on the selected file's open handle, including for uploaded and nested RARs.
type rarSource struct {
	archive *Archive
	ctx     context.Context
	listing bool
}

func (s *rarSource) Open(name string) (fs.File, error) {
	if name != rarSourceName {
		return nil, fs.ErrNotExist
	}
	file := s.archive.rarSection(s.ctx)
	if s.listing {
		file.remaining = formats.RARDirectoryReadBytes
	}
	return file, nil
}

type rarSection struct {
	*io.SectionReader
	ctx       context.Context
	stat      fs.FileInfo
	remaining int64
}

func (a *Archive) rarSection(ctx context.Context) *rarSection {
	return &rarSection{
		SectionReader: io.NewSectionReader(contextReaderAt{ctx, a.f}, 0, a.stat.Size()),
		ctx:           ctx,
		stat:          a.stat,
		remaining:     -1,
	}
}

func (f *rarSection) Close() error               { return nil }
func (f *rarSection) Stat() (fs.FileInfo, error) { return f.stat, nil }

func (f *rarSection) Read(p []byte) (int, error) {
	if f.remaining < 0 {
		return f.SectionReader.Read(p)
	}
	if f.remaining == 0 {
		return 0, fmt.Errorf("RAR 目录读取超过 %d MiB", formats.RARDirectoryReadBytes>>20)
	}
	// Limit read-ahead while skipping packed payloads in the directory pass.
	n, err := f.SectionReader.Read(p[:min(int64(len(p)), f.remaining, 64)])
	f.remaining -= int64(n)
	return n, err
}

func (f *rarSection) Seek(offset int64, whence int) (int64, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	pos, err := f.SectionReader.Seek(offset, whence)
	if err == nil && pos > f.Size() {
		return 0, io.ErrUnexpectedEOF
	}
	return pos, err
}

func (a *Archive) rarCatalogue(ctx context.Context) ([]*rardecode.File, error) {
	source := &rarSource{archive: a, ctx: ctx, listing: true}
	files, err := rardecode.List(rarSourceName, rardecode.FileSystem(source),
		rardecode.BufferSize(rarBufferBytes), rardecode.MaxDictionarySize(formats.RARDecodeDictionaryBytes))
	if errors.Is(err, fs.ErrNotExist) {
		err = rardecode.ErrMultiVolume
	}
	if err != nil {
		return nil, rarReadError(err)
	}
	if len(files) > MaxEntries {
		return nil, errors.New("压缩包成员数量超过 200,000")
	}
	// A subsequent File.Open is an extraction, with the ordinary member limit.
	source.listing = false
	return files, nil
}

func rarMemberSize(h *rardecode.FileHeader) int64 {
	if h.UnKnownSize {
		return -1
	}
	return h.UnPackedSize
}

func (a *Archive) listRAR(ctx context.Context) error {
	files, err := a.rarCatalogue(ctx)
	if err != nil {
		return err
	}
	for i, entry := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.PackedSize < 0 || (!entry.UnKnownSize && entry.UnPackedSize < 0) {
			return errors.New("RAR 成员大小无效")
		}
		regular := entry.Mode().IsRegular() && entry.LinkType == 0
		if entry.HostOS == rardecode.HostOSUnix {
			kind := entry.Attributes & 0170000
			regular = regular && (kind == 0 || kind == 0100000)
		}
		a.add(entry.Name, rarMemberSize(&entry.FileHeader), uint64(entry.PackedSize), i, 0, 0, regular)
	}
	return a.unchanged()
}

func (a *Archive) openRARMember(ctx context.Context, m Member, progress Progress) (io.Reader, io.Closer, error) {
	if progress != nil {
		progress(0, m.Size, "正在定位 RAR 成员")
	}
	// Use a fresh catalogue so the reader checks the selected member's checksum
	// on every extraction and uses this operation's cancellation context.
	files, err := a.rarCatalogue(ctx)
	if err != nil {
		return nil, nil, err
	}
	if m.index >= len(files) {
		return nil, nil, errors.New("压缩包成员目录已改变")
	}
	entry := files[m.index]
	if strings.ReplaceAll(entry.Name, "\\", "/") != m.Name || rarMemberSize(&entry.FileHeader) != m.Size {
		return nil, nil, errors.New("压缩包成员目录已改变")
	}
	if entry.Encrypted {
		return nil, nil, rarReadError(rardecode.ErrArchivedFileEncrypted)
	}
	if !entry.Solid {
		reader, err := entry.Open()
		return reader, reader, rarReadError(err)
	}
	if progress != nil {
		progress(0, m.Size, "正在顺序读取固实 RAR 数据")
	}
	reader, err := a.openSolidRAR(ctx, files[:m.index])
	return reader, nil, rarReadError(err)
}

func (a *Archive) openSolidRAR(ctx context.Context, prefix []*rardecode.File) (*rardecode.Reader, error) {
	var total int64
	for _, entry := range prefix {
		size := rarMemberSize(&entry.FileHeader)
		if size < 0 {
			return nil, errors.New("RAR 固实前置成员大小未知，无法安全定位")
		}
		if size > MaxContainerBytes-total {
			return nil, errors.New("RAR 固实前置成员声明大小超过 64 GiB")
		}
		total += size
	}
	reader, err := rardecode.NewReader(a.rarSection(ctx),
		rardecode.BufferSize(rarBufferBytes), rardecode.MaxDictionarySize(formats.RARDecodeDictionaryBytes))
	if err != nil {
		return nil, err
	}
	for range len(prefix) + 1 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := reader.Next(); err != nil {
			return nil, err
		}
	}
	return reader, nil
}

func rarReadError(err error) error {
	switch {
	case errors.Is(err, rardecode.ErrArchiveEncrypted), errors.Is(err, rardecode.ErrArchivedFileEncrypted):
		return fmt.Errorf("RAR 已加密，请先解密所需文件：%w", err)
	case errors.Is(err, rardecode.ErrMultiVolume), errors.Is(err, rardecode.ErrBadVolumeNumber):
		return fmt.Errorf("当前仅能读取单文件 RAR，请先将分卷解压或合为单个压缩包：%w", err)
	case errors.Is(err, rardecode.ErrDictionaryTooLarge):
		return fmt.Errorf("RAR 解码字典超过 %d MiB：%w", formats.RARDecodeDictionaryBytes>>20, err)
	case errors.Is(err, rardecode.ErrNoSig):
		return fmt.Errorf("文件内容不是可识别的 RAR：%w", err)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, rardecode.ErrUnexpectedArcEnd), errors.Is(err, rardecode.ErrShortFile):
		return fmt.Errorf("RAR 不完整，请重新获取完整压缩包：%w", err)
	case errors.Is(err, rardecode.ErrBadFileChecksum), errors.Is(err, rardecode.ErrBadHeaderCRC):
		return fmt.Errorf("RAR 数据校验失败：%w", err)
	default:
		return err
	}
}
