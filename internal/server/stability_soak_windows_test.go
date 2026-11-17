package server

import (
	"fmt"
	"syscall"
	"unsafe"
)

func init() {
	getMemory := syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")
	getHandles := syscall.NewLazyDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	stabilityProcessMemory = func() (stabilityOSMemory, error) {
		process, err := syscall.GetCurrentProcess()
		if err != nil {
			return stabilityOSMemory{}, err
		}
		var counters struct {
			Size, PageFaults                                                                                      uint32
			PeakWorkingSet, WorkingSet, PeakPaged, Paged, PeakNonPaged, NonPaged, Pagefile, PeakPagefile, Private uintptr
		}
		counters.Size = uint32(unsafe.Sizeof(counters))
		ok, _, failure := getMemory.Call(uintptr(process), uintptr(unsafe.Pointer(&counters)), uintptr(counters.Size))
		if ok == 0 {
			return stabilityOSMemory{}, fmt.Errorf("GetProcessMemoryInfo: %w", failure)
		}
		var handles uint32
		ok, _, failure = getHandles.Call(uintptr(process), uintptr(unsafe.Pointer(&handles)))
		if ok == 0 {
			return stabilityOSMemory{}, fmt.Errorf("GetProcessHandleCount: %w", failure)
		}
		return stabilityOSMemory{PrivateBytes: uint64(counters.Private), WorkingSetBytes: uint64(counters.WorkingSet), Handles: handles}, nil
	}
}
