package localstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/minio/sio"
	"golang.org/x/crypto/argon2"
)

// Only the format identifier, salt and opaque DARE streams are public.
// The manifest authenticates file names, sizes and plaintext digests.
const volumeHeader = "ExcelLens-DARE2-AES256GCM-Argon2id-v1\n"
const manifestLimit = 128 << 20

type backupVolume struct {
	root *os.Root
	key  []byte
}

func newVolume(root *os.Root, password string, create bool) (backupVolume, error) {
	v := backupVolume{root: root}
	var salt [16]byte
	if create {
		if password == "" {
			return v, nil
		}
		rand.Read(salt[:])
		if err := writeDurable(root, "encryption", append([]byte(volumeHeader), salt[:]...)); err != nil {
			return v, err
		}
	} else {
		header, err := readBounded(root, "encryption", int64(len(volumeHeader)+len(salt)))
		if errors.Is(err, os.ErrNotExist) && password == "" {
			return v, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return v, errors.New("备份缺少加密头，请核对目录和口令")
		}
		if err != nil {
			return v, err
		}
		if len(header) != len(volumeHeader)+len(salt) || !bytes.HasPrefix(header, []byte(volumeHeader)) {
			return v, errors.New("加密备份头无效")
		}
		if password == "" {
			return v, errors.New("请输入加密备份的口令")
		}
		copy(salt[:], header[len(volumeHeader):])
	}
	v.key = argon2.IDKey([]byte(password), salt[:], 3, 64*1024, 1, 32)
	return v, nil
}
func (v backupVolume) derive(label, name string) []byte {
	m := hmac.New(sha256.New, v.key)
	io.WriteString(m, label+"\x00"+name)
	return m.Sum(nil)
}
func (v backupVolume) object(name string) (string, sio.Config) {
	if v.key == nil {
		return name, sio.Config{}
	}
	return "objects/" + hex.EncodeToString(v.derive("name", name)), sio.Config{MinVersion: sio.Version20, MaxVersion: sio.Version20, CipherSuites: []byte{sio.AES_GCM}, Key: v.derive("key", name)}
}
func (v backupVolume) write(name string, src io.Reader) error {
	out, config := v.object(name)
	return writeNew(v.root, out, func(f *os.File) error {
		if v.key != nil {
			_, err := sio.Encrypt(f, io.MultiReader(bytes.NewReader([]byte{1}), src), config)
			return err
		}
		_, err := io.Copy(f, src)
		return err
	})
}
func (v backupVolume) read(name string) (io.ReadCloser, error) {
	path, config := v.object(name)
	f, _, err := openRegular(v.root, path)
	if err != nil {
		return nil, err
	}
	if v.key == nil {
		return f, nil
	}
	reader, err := sio.DecryptReader(f, config)
	// Include an authenticated format byte even for empty source files.
	if err == nil {
		var marker [1]byte
		_, err = io.ReadFull(reader, marker[:])
		if err == nil && marker[0] != 1 {
			err = errors.New("加密对象头无效")
		}
	}

	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{reader, f}, nil
}
func (v backupVolume) manifest() ([]byte, error) {
	f, err := v.read("manifest.json")
	if err != nil && v.key != nil {
		return nil, fmt.Errorf("口令错误或备份清单受损：%w", err)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, manifestLimit+1))
	if err != nil && v.key != nil {
		return nil, fmt.Errorf("口令错误或备份清单受损：%w", err)
	}
	if len(data) > manifestLimit {
		return nil, errors.New("备份清单超过容量")
	}
	return data, err
}
func writeNew(root *os.Root, name string, fill func(*os.File) error) error {
	name = filepath.FromSlash(name)
	if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = fill(f)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
func writeDurable(root *os.Root, name string, data []byte) error {
	return writeNew(root, name, func(f *os.File) error { _, err := f.Write(data); return err })
}
func readBounded(root *os.Root, name string, limit int64) ([]byte, error) {
	f, st, err := openRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st.Size() > limit {
		return nil, errors.New("记录过大")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("记录过大")
	}
	return b, err
}
func (v backupVolume) verify(ctx context.Context, name string, want fileRecord, dst io.Writer) error {
	src, err := v.read(name)
	if err != nil {
		return err
	}
	defer src.Close()
	got, err := digestCopy(ctx, io.LimitReader(src, want.Bytes+1), dst)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("备份校验失败：%s", name)
	}
	return nil
}
