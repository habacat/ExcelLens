package server

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

	"excellens/internal/localstore"
)

// Saved user configurations live in the config directory and never in cache
// maintenance roots. Views and recipes share only this persistence envelope.
const maxConfigRevision = 1<<53 - 1

type savedConfigEnvelope struct {
	Version int             `json:"version"`
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}

func savedConfigPath(kind, name string) (string, error) {
	root, err := localstore.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, kind, name+".json"), nil
}

func decodeSavedConfig(data []byte, dst any) error {
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

func readSavedConfig(path string, maxBytes int64, dst any) (bool, error) {
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
		return false, errors.New("本地设置文件超过容量，原文件已保留")
	}
	var envelope savedConfigEnvelope
	if err := decodeSavedConfig(data, &envelope); err != nil {
		return false, fmt.Errorf("本地设置文件损坏，原文件已保留：%w", err)
	}
	if envelope.Version != 1 {
		return false, errors.New("不支持此版本的本地设置，原文件已保留")
	}
	digest := sha256.Sum256(envelope.Payload)
	if envelope.SHA256 != fmt.Sprintf("%x", digest[:]) {
		return false, errors.New("本地设置校验失败，原文件已保留")
	}
	if err := decodeSavedConfig(envelope.Payload, dst); err != nil {
		return false, fmt.Errorf("本地设置结构无效，原文件已保留：%w", err)
	}
	return true, nil
}

type configWriteOps struct {
	sync   func(*os.File) error
	rename func(string, string) error
}

var durableConfigOps = configWriteOps{sync: (*os.File).Sync, rename: os.Rename}

// Never delete the old destination to work around a failed replacement. A
// failed Sync/rename must leave the previous confirmed configuration intact.
// Caller serializes the entire read/modify/write transaction by file path.
func writeSavedConfig(path string, maxBytes int64, value any, ops configWriteOps) error {
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
	if err := ops.sync(f); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return ops.rename(f.Name(), path)
}
