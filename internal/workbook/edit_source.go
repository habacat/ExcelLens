package workbook

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"excellens/internal/formats"
)

// EditDialect carries the adapter's selected dialect without rerunning or
// modifying format detection. The editor verifies exact record boundaries.
func (w *Workbook) EditDialect() (byte, string) {
	if w.source == nil || w.source.delimiter <= 0 || w.source.delimiter > 127 {
		return 0, ""
	}
	return byte(w.source.delimiter), w.source.encoding
}

// EditSourceFile holds a read-only source handle for one explicit operation.
// Windows excludes file writers. Elsewhere callers also verify the content
// actually consumed before publishing outputs.
type EditSourceFile struct {
	*os.File
	info    os.FileInfo
	stamp   externalFileStamp
	stamped bool
}

func OpenEditSource(path string) (*EditSourceFile, error) {
	expected, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !expected.Mode().IsRegular() {
		return nil, errors.New("编辑源必须是普通文件")
	}
	f, stable, err := openFingerprintSource(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		switch {
		case !os.SameFile(expected, info):
			err = errors.New("编辑源在打开时发生了变化")
		case !info.Mode().IsRegular(), info.Size() == 0:
			err = errors.New("编辑源必须是非空的普通文件")
		case info.Size() > formats.EditSourceBytes:
			err = fmt.Errorf("编辑源超过 %d MB 上限", formats.EditSourceBytes>>20)
		case !stable && os.PathSeparator == '\\':
			err = errors.New("无法锁定源文件以读取固定版本。请关闭正在写入该文件的程序后重试")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	src := &EditSourceFile{File: f, info: info}
	if stable {
		src.stamp, src.stamped = fingerprintSourceStamp(f, info)
	}
	return src, nil
}

func (f *EditSourceFile) Size() int64 { return f.info.Size() }

func (f *EditSourceFile) Check() error {
	info, err := os.Stat(f.Name())
	if err != nil || !os.SameFile(f.info, info) || info.Size() != f.Size() || !info.ModTime().Equal(f.info.ModTime()) {
		return errors.New("编辑源已变更或被移走，请打开原版本后重试")
	}
	if f.stamped {
		if stamp, ok := fingerprintSourceStamp(f.File, info); !ok || stamp != f.stamp {
			return errors.New("编辑源在读取时发生了变化")
		}
	}
	return nil
}

func (f *EditSourceFile) SHA256(ctx context.Context) (string, error) {
	return f.digest(ctx, "edit-raw-sha256-v1", "")
}

func (f *EditSourceFile) digest(ctx context.Context, domain, prefix string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f.stamped {
		if cached, ok := recalledSourceFingerprint(f.stamp, domain); ok {
			return cached, f.Check()
		}
	}
	h := sha256.New()
	_, _ = io.WriteString(h, prefix)
	source := io.NewSectionReader(f.File, 0, f.Size())
	buf := make([]byte, 256<<10)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := source.Read(buf)
		_, _ = h.Write(buf[:n])
		read += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if read != f.Size() {
		return "", io.ErrUnexpectedEOF
	}
	if err := f.Check(); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if f.stamped {
		rememberSourceFingerprint(f.stamp, domain, digest)
	}
	return digest, nil
}

func (w *Workbook) CheckEditSource(ctx context.Context, f *EditSourceFile) error {
	if filepath.Clean(f.Name()) != filepath.Clean(w.Path) {
		return errors.New("编辑源路径与当前文件不一致")
	}
	var fingerprint string
	if w.source != nil && w.source.fingerprintDomain != "" {
		domain := w.source.fingerprintDomain
		var err error
		fingerprint, err = f.digest(ctx, domain, "ExcelLens-external-source-v2:\x00"+domain+"\x00")
		if err != nil {
			return err
		}
	} else {
		zipFile, err := zip.NewReader(f, f.Size())
		if err != nil {
			return err
		}
		fingerprint = fingerprintZip(zipFile.File)
	}
	if fingerprint != w.Fingerprint {
		return errors.New("编辑源与当前视图的版本不同，请重新打开数据文件")
	}
	return f.Check()
}
