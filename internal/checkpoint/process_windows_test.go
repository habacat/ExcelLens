//go:build windows

package checkpoint

import (
	"os"
	"os/exec"
	"syscall"
)

func hideTestProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

func markSparse(file *os.File) error {
	var returned uint32
	return syscall.DeviceIoControl(syscall.Handle(file.Fd()), 0x900c4, nil, 0, nil, 0, &returned, nil)
}
