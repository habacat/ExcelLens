//go:build !windows

package instance

// The desktop single-instance contract applies to the Windows tray build.
// Other platforms run the HTTP-server-only development/QA implementation.
func Acquire() (bool, func(), error) { return true, func() {}, nil }
