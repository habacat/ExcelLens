//go:build openbsd

package localstore

import (
	"fmt"
	"os"
	"syscall"
)

func availableSpace(path string) (volumeSpace, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return volumeSpace{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return volumeSpace{}, err
	}
	block := uint64(fs.F_bsize)
	var free uint64
	if fs.F_bavail > 0 {
		free = uint64(fs.F_bavail) * block
	}
	return volumeSpace{fmt.Sprint(st.Sys().(*syscall.Stat_t).Dev), free, block}, nil
}
