//go:build !windows

package checkpoint

import (
	"os"
	"os/exec"
)

func hideTestProcess(*exec.Cmd) {}
func markSparse(*os.File) error { return nil }
