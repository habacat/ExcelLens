// Package savedconfig reads and atomically replaces checksummed local JSON settings.
package savedconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

type savedConfigEnvelope struct {
	Version int             `json:"version"`
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}

func Decode(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return errors.New("设置内容不是有效UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("设置内容包含多余数据")
	}
	return nil
}

func Read(path string, maxBytes int64, dst any) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return false, err
	}
	if int64(len(data)) > maxBytes {
		return false, fmt.Errorf("%w：文件超过容量，原文件已保留", ErrDamaged)
	}
	return decodeEnvelope(data, dst)
}

var ErrDamaged = errors.New("本地设置文件损坏")

func decodeEnvelope(data []byte, dst any) (bool, error) {
	// A newer valid envelope can have additional fields; never offer to repair it.
	var version struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(data, &version) == nil && version.Version > 1 {
		return false, errors.New("不支持此版本的本地设置，原文件已保留")
	}
	var envelope savedConfigEnvelope
	if err := Decode(data, &envelope); err != nil {
		return false, fmt.Errorf("%w，原文件已保留：%w", ErrDamaged, err)
	}
	if envelope.Version == 0 {
		return false, fmt.Errorf("%w：缺少版本", ErrDamaged)
	}
	if envelope.Version != 1 {
		return false, errors.New("不支持此版本的本地设置，原文件已保留")
	}
	digest := sha256.Sum256(envelope.Payload)
	if envelope.SHA256 != fmt.Sprintf("%x", digest[:]) {
		return false, fmt.Errorf("%w：本地设置校验失败，原文件已保留", ErrDamaged)
	}
	if err := Decode(envelope.Payload, dst); err != nil {
		return false, fmt.Errorf("%w：本地设置结构无效，原文件已保留：%w", ErrDamaged, err)
	}
	return true, nil
}

type WriteOps struct {
	Sync   func(*os.File) error
	Rename func(string, string) error
}

var Durable = WriteOps{Sync: (*os.File).Sync, Rename: os.Rename}

// Never delete the old destination to work around a failed replacement. A
// failed Sync/rename must leave the previous confirmed configuration intact.
// Caller serializes the entire read/modify/write transaction by file path.
func Write(path string, maxBytes int64, value any, ops WriteOps) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	data, err := json.Marshal(savedConfigEnvelope{Version: 1, SHA256: fmt.Sprintf("%x", digest[:]), Payload: payload})
	if err != nil {
		return err
	}
	if int64(len(data))+1 > maxBytes {
		return errors.New("本地设置达到容量，请先删除不用的项目")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".saved-config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := ops.Sync(f); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return ops.Rename(f.Name(), path)
}
