//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package localstore

import (
	"io/fs"
	"os"

	"excellens/internal/i18n"
)

func allocatedSize(_ *os.Root, _ string, _ fs.FileInfo) (int64, error) {
	return 0, i18n.Error("diagnostic.localstore.sizeOther.thisPlatformCannotReadFile")
}
