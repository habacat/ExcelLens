//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package localstore

import "errors"

func availableSpace(string) (volumeSpace, error) {
	return volumeSpace{}, errors.New("当前系统无法核验可用空间")
}
