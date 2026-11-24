package localstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/sio"
)

// A reserve covers filesystem bookkeeping and bounded transfer receipts.
// Journal, manifest and staged payloads are budgeted separately.
const spaceReserve = 4 << 20

type volumeSpace struct {
	id    string
	free  uint64
	block uint64
}
type spaceProbe func(string) (volumeSpace, error)
type diskNeed struct {
	path      string
	remaining uint64
	block     uint64
	since     uint64
	next      time.Time
	probe     spaceProbe
}
type transferSpace struct {
	volumes map[string]*diskNeed
	targets map[string]*diskNeed
	planned map[string]fileRecord
}

func existingDir(name string) (string, error) {
	for {
		st, err := os.Stat(name)
		if err == nil {
			if !st.IsDir() {
				return "", fmt.Errorf("不是目录：%s", name)
			}
			return name, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(name)
		if parent == name {
			return "", err
		}
		name = parent
	}
}
func planVolumes(paths map[string]string, probe spaceProbe) (*transferSpace, error) {
	if probe == nil {
		probe = availableSpace
	}
	s := &transferSpace{volumes: map[string]*diskNeed{}, targets: map[string]*diskNeed{}}
	for target, name := range paths {
		dir, err := existingDir(name)
		if err != nil {
			return nil, err
		}
		info, err := probe(dir)
		if err != nil {
			return nil, fmt.Errorf("无法核验 %s 的可用空间：%w", dir, err)
		}
		if info.id == "" || info.block == 0 {
			return nil, fmt.Errorf("无法确定磁盘和分配单位：%s", dir)
		}
		v := s.volumes[info.id]
		if v == nil {
			v = &diskNeed{path: dir, block: info.block, probe: probe}
			s.volumes[info.id] = v
		}
		s.targets[target] = v
	}
	return s, nil
}
func (v *diskNeed) add(bytes uint64) error {
	if bytes > math.MaxInt64-v.remaining {
		return errors.New("传输容量超过支持范围")
	}
	v.remaining += bytes
	return nil
}
func (v *diskNeed) file(bytes uint64) error {
	// Round the contents to allocation units and allow a directory entry.
	if bytes > math.MaxInt64-v.block {
		return errors.New("文件容量超过支持范围")
	}
	allocated := bytes
	if rem := bytes % v.block; rem != 0 {
		allocated += v.block - rem
	}
	return v.add(allocated + v.block)
}
func diskBytes(n uint64) string {
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	value := float64(n) / 1024
	i := 0
	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}
func (v *diskNeed) check() error {
	info, err := v.probe(v.path)
	if err != nil {
		return fmt.Errorf("无法核验 %s 的剩余空间：%w", v.path, err)
	}
	if info.free < v.remaining {
		return fmt.Errorf("磁盘空间不足（%s）：需要 %s，可用 %s", v.path, diskBytes(v.remaining), diskBytes(info.free))
	}
	v.next = time.Now().Add(time.Second)
	v.since = 0
	return nil
}
func (s *transferSpace) check() error {
	for _, v := range s.volumes {
		if err := v.check(); err != nil {
			return err
		}
	}
	return nil
}

type spaceWriter struct {
	target io.Writer
	disk   *diskNeed
}

func (w spaceWriter) Write(data []byte) (int, error) {
	v := w.disk
	if v.since >= 8<<20 || !time.Now().Before(v.next) {
		if err := v.check(); err != nil {
			return 0, err
		}
	}
	n, err := w.target.Write(data)
	v.remaining -= min(uint64(n), v.remaining)
	v.since += uint64(n)
	return n, err
}
func (v *diskNeed) writer(dst io.Writer) io.Writer {
	if v == nil {
		return dst
	}
	return spaceWriter{dst, v}
}

func encodedSize(bytes int64, encrypted bool) (uint64, error) {
	if bytes < 0 || bytes == math.MaxInt64 {
		return 0, errors.New("无效的文件大小")
	}
	if !encrypted {
		return uint64(bytes), nil
	}
	return sio.EncryptedSize(uint64(bytes) + 1)
}

func (r Roots) backupSpace(ctx context.Context, parent string, o TransferOptions) (*transferSpace, int64, int, error) {
	plan, err := planVolumes(map[string]string{"backup": parent, "config": r.Config, "local": r.Local}, o.spaceProbe)
	if err != nil {
		return nil, 0, 0, err
	}
	dst := plan.targets["backup"]
	var total int64
	sizes := map[string]fileRecord{}
	digest := strings.Repeat("0", 64)
	for _, g := range r.groups() {
		if !g.backup {
			continue
		}
		err := r.walk(ctx, g, true, func(_ *os.Root, name string, st fs.FileInfo) error {
			n := st.Size()
			size, err := encodedSize(n, o.Password != "")
			if err != nil {
				return err
			}
			if n > math.MaxInt64-total {
				return errors.New("备份字节数超过容量")
			}
			total += n
			sizes[g.base+"/"+name] = fileRecord{n, digest}
			return dst.file(size)
		})
		if err != nil {
			return nil, 0, 0, err
		}
	}
	data, err := json.MarshalIndent(manifest{1, sizes}, "", "  ")
	if err != nil {
		return nil, 0, 0, err
	}
	if len(data) > manifestLimit {
		return nil, 0, 0, errors.New("备份清单超过容量")
	}
	size, err := encodedSize(int64(len(data)), o.Password != "")
	if err == nil {
		err = dst.file(size)
	}
	if err == nil && o.Password != "" {
		err = dst.file(uint64(len(volumeHeader) + 16))
	}
	if err == nil {
		err = dst.add(spaceReserve)
	}
	if err == nil {
		err = plan.targets["config"].add(1 << 20)
	}
	if err == nil {
		err = plan.check()
	}
	plan.planned = sizes
	return plan, total, len(sizes), err
}

func (r Roots) restoreSpace(ctx context.Context, units []restoreUnit, files map[string]fileRecord, o TransferOptions) (*transferSpace, error) {
	plan, err := planVolumes(map[string]string{"config": r.Config, "local": r.Local}, o.spaceProbe)
	if err != nil {
		return nil, err
	}
	missing := map[string]fileRecord{}
	for _, u := range units {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(filepath.Join(r.directory(u.base), u.rel)); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, key := range u.names {
			rec := files[key]
			if err := plan.targets[u.base].file(uint64(rec.Bytes)); err != nil {
				return nil, err
			}
			missing[key] = rec
		}
	}
	journal, err := json.Marshal(restorePlan{strings.Repeat("0", 32), "commit", missing})
	if err != nil {
		return nil, err
	}
	if len(journal) > manifestLimit {
		return nil, errors.New("恢复事务超过容量")
	}
	if err = plan.targets["config"].add(uint64(len(journal)) + 1<<20); err != nil {
		return nil, err
	}
	for _, v := range plan.volumes {
		if err = v.add(spaceReserve); err != nil {
			return nil, err
		}
	}
	plan.planned = missing
	return plan, plan.check()
}
