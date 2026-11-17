//go:build windows

package workbook

import "syscall"

func queryProcessAlive(pid int) bool {
	handle, err := syscall.OpenProcess(syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// Invalid PID means ended. Access denied and other uncertain states
		// are treated as live; cleanup never terminates or signals processes.
		return err != syscall.Errno(87)
	}
	defer syscall.CloseHandle(handle)
	state, err := syscall.WaitForSingleObject(handle, 0)
	return err != nil || state != syscall.WAIT_OBJECT_0
}
