//go:build linux || darwin || freebsd || dragonfly

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
	block := uint64(fs.Bsize)
	var free uint64
	if fs.Bavail > 0 {
		free = uint64(fs.Bavail) * block
	}
	return volumeSpace{fmt.Sprint(st.Sys().(*syscall.Stat_t).Dev), free, block}, nil
}
