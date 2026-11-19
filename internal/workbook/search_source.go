package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"

	"excellens/internal/formats"
)

// SearchSourceStamp binds an explicitly requested search to the precise index
// and row stream it reads. It never builds an index or interprets source values.
// Reliable file revisions avoid rereading the confirmed prefix after a process
// restart. On other filesystems the conservative fallback hashes cache bytes.
// The caller owns its ordinary cache read lock and Workbook pin throughout.
func (w *Workbook) SearchSourceStamp(ctx context.Context, meta SheetMeta, idx *SheetIndex) (string, error) {
	return w.SourceIndexStamp(ctx, meta, idx)
}

// SourceIndexStamp is shared by read-only query drivers. The caller retains
// the Workbook/cache read lease; no query rules or saved row orders are inferred.
func (w *Workbook) SourceIndexStamp(ctx context.Context, meta SheetMeta, idx *SheetIndex) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	actual, found := w.FindSheet(meta.Name)
	w.indexMu.RLock()
	prepared := w.indexes[meta.Path]
	w.indexMu.RUnlock()
	if !found || actual.Path != meta.Path || idx == nil || idx != prepared || idx.SheetPath != meta.Path {
		return "", errors.New("搜索来源的精确索引已变化，请重新运行")
	}
	if err := w.CheckSourceUnchanged(); err != nil {
		return "", err
	}
	index, err := searchCacheStamp(ctx, idx.IndexPath, -1)
	if err != nil {
		return "", err
	}
	rows, err := searchCacheStamp(ctx, idx.XMLPath, idx.XMLSize)
	if err != nil {
		return "", err
	}
	semantics := 1
	if len(idx.SharedAnchors) > 0 {
		semantics = 2 // Shared formula text now translates whole row/column references.
	}
	encoded, err := json.Marshal([]any{semantics, w.Fingerprint, w.Format, meta.Name, meta.Path, w.Date1904, idx.RowCount(), idx.MaxRow, idx.MaxCol, idx.XMLSize, index, rows})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func searchCacheStamp(ctx context.Context, path string, expectedSize int64) (string, error) {
	f, stable, err := openFingerprintSource(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("搜索缓存不是普通文件")
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		return "", errors.New("搜索缓存长度与来源索引不一致，请重新运行")
	}
	stamp, reliable := fingerprintSourceStamp(f, info)
	reliable = reliable && stable
	var result string
	if reliable {
		data, err := json.Marshal(stamp)
		if err != nil {
			return "", err
		}
		result = "revision:" + string(data)
	} else {
		digest := sha256.New()
		buffer := make([]byte, formats.CheckpointIOBytes)
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			n, readErr := f.Read(buffer)
			_, _ = digest.Write(buffer[:n])
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return "", readErr
			}
		}
		result = "sha256:" + hex.EncodeToString(digest.Sum(nil))
	}
	after, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return "", errors.New("搜索缓存在绑定时发生变化，请重新运行")
	}
	if reliable {
		current, ok := fingerprintSourceStamp(f, after)
		if !ok || current != stamp {
			return "", errors.New("搜索缓存修订已变化，请重新运行")
		}
	}
	return result, ctx.Err()
}
