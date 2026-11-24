//go:build windows

package checkpoint

import "os"

// Windows has no portable directory-fsync through os.Root. The data and both
// manifest slots are individually synced before the atomic Root.Rename.
// Recovery validates the resulting slot; no filesystem-wide power-loss promise.
func syncDirectory(*os.Root, string) error { return nil }
