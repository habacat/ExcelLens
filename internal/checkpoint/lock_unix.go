//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package checkpoint

import "os"

func syncDirectory(root *os.Root, name string) error {
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
