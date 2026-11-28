//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package localstore

import (
	"excellens/internal/i18n"
)

func availableSpace(string) (volumeSpace, error) {
	return volumeSpace{}, i18n.Error("diagnostic.localstore.spaceOther.systemCannotVerifyAvailableSpace")
}
