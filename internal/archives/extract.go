package archives

import (
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
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
	"github.com/bodgit/sevenzip"
)

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

func cacheMemberTarget(dir, key string) (string, [32]byte) {
	digest := sha256.Sum256([]byte(key))
	target := filepath.Join(dir, hex.EncodeToString(digest[:16])+strings.ToLower(filepath.Ext(key)))
	return target, digest
}

func reuseCachedMember(target string, expected int64, progress Progress) bool {
	raw, err := os.ReadFile(target + ".json")
	if err != nil {
		return false
	}
	var receipt cachedMember
	if json.Unmarshal(raw, &receipt) != nil {
		return false
	}
	st, err := os.Stat(target)
	if err != nil || !st.Mode().IsRegular() || st.Size() != receipt.Size || st.ModTime().UnixNano() != receipt.Modified {
		return false
	}
	if expected >= 0 && receipt.Size != expected {
		return false
	}
	if progress != nil {
		progress(st.Size(), st.Size(), "已复用压缩包成员缓存")
	}
	return true
}

func (a *Archive) cacheStream(ctx context.Context, key string, src io.Reader, limit, expected int64, crc uint32, progress Progress) (string, error) {
	dir, err := a.ensureCache(ctx, progress)
	if err != nil {
		return "", err
	}
	target, digest := cacheMemberTarget(dir, key)
	gate := cacheShards[digest[0]%32]
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if reuseCachedMember(target, expected, progress) {
		return target, nil
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
		if format, ok := formats.FormatForExtension(filepath.Ext(m.Name)); ok && format == "sqlite" {
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
	dir, err := a.ensureCache(ctx, progress)
	if err != nil {
		return "", m, err
	}
	if target, _ := cacheMemberTarget(dir, key); reuseCachedMember(target, m.Size, progress) {
		return target, m, nil
	}
	var src io.Reader
	var closer io.Closer
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
