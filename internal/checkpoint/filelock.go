package checkpoint

import (
	"errors"
	"os"

	"excellens/internal/filelock"
)

func lockFile(f *os.File) error {
	err := filelock.Lock(f)
	if errors.Is(err, filelock.ErrBusy) {
		return ErrBusy
	}
	return err
}
func unlockFile(f *os.File) error { return filelock.Unlock(f) }
