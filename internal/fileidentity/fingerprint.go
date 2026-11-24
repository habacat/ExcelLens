// Package fileidentity binds read-only sources to verified bytes and file revisions.
package fileidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

func Fingerprint(path string, ext string) (string, os.FileInfo, error) {
	f, stable, err := Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("读取数据文件失败：%w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() {
		return "", nil, errors.New("所选路径不是普通文件")
	}
	// SQLite and its WAL form a live multi-file source; they always use the
	// full existing content hash rather than a single-file memo receipt.
	var stamp Stamp
	stampOK := false
	if stable && ext != "sqlite" {
		stamp, stampOK = Revision(f, info)
	}
	checkSource := func() error {
		current, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return errors.New("数据文件在打开时发生了变化，请重新打开")
		}
		if stampOK {
			after, ok := Revision(f, current)
			if !ok || after != stamp {
				return errors.New("数据文件在校验时发生了变化，请重新打开")
			}
		}
		return nil
	}
	if stampOK {
		if digest, ok := Recall(stamp, ext); ok {
			if err := checkSource(); err != nil {
				return "", nil, err
			}
			return digest, info, nil
		}
	}
	h := sha256.New()
	_, _ = io.WriteString(h, "ExcelLens-external-source-v2:\x00"+ext+"\x00")
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", nil, err
	}
	// A SQLite WAL can contain committed rows not yet checkpointed into DB.
	// Including it in the fingerprint prevents stale cached views for local DBs.
	if ext == "sqlite" {
		wal := path + "-wal"
		if wf, err := os.Open(wal); err == nil {
			_, _ = io.WriteString(h, "\x00WAL\x00")
			_, copyErr := io.CopyBuffer(h, wf, make([]byte, 1<<20))
			closeErr := wf.Close()
			if copyErr != nil {
				return "", nil, copyErr
			}
			if closeErr != nil {
				return "", nil, closeErr
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("无法访问 SQLite WAL 日志文件：%w", err)
		}
	}
	if err := checkSource(); err != nil {
		return "", nil, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if stampOK {
		Remember(stamp, ext, digest)
	}
	return digest, info, nil
}
