//go:build windows

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"excellens/internal/formats"
)

var clipUser = syscall.NewLazyDLL("user32.dll")
var clipKernel = syscall.NewLazyDLL("kernel32.dll")
var openClipboard = clipUser.NewProc("OpenClipboard")
var closeClipboard = clipUser.NewProc("CloseClipboard")
var getClipboardData = clipUser.NewProc("GetClipboardData")
var registerClipboardFormat = clipUser.NewProc("RegisterClipboardFormatW")
var clipboardAvailable = clipUser.NewProc("IsClipboardFormatAvailable")
var clipboardLock = clipKernel.NewProc("GlobalLock")
var clipboardUnlock = clipKernel.NewProc("GlobalUnlock")
var clipboardSize = clipKernel.NewProc("GlobalSize")

func clipFormat(name string) uintptr {
	p, _ := syscall.UTF16PtrFromString(name)
	id, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(p)))
	return id
}

func clipboardBytes(id uintptr, limit int) ([]byte, error) {
	h, _, _ := getClipboardData.Call(id)
	if h == 0 {
		return nil, errors.New("Excel 尚未提供剪贴板数据，请重新复制")
	}
	n, _, _ := clipboardSize.Call(h)
	if n == 0 || n > uintptr(limit) {
		return nil, errEditRequestCapacity
	}
	ptr, _, _ := clipboardLock.Call(h)
	if ptr == 0 {
		return nil, errors.New("剪贴板正忙，请重试")
	}
	defer clipboardUnlock.Call(h)
	out := make([]byte, int(n))
	// Copy from the locked HGLOBAL while it belongs to this clipboard. Let
	// Windows validate the buffer instead of converting its address to a Go pointer.
	var copied uintptr
	process, _, _ := clipKernel.NewProc("GetCurrentProcess").Call()
	ok, _, _ := clipKernel.NewProc("ReadProcessMemory").Call(process, ptr, uintptr(unsafe.Pointer(&out[0])), n, uintptr(unsafe.Pointer(&copied)))
	runtime.KeepAlive(out)
	if ok == 0 || copied != n {
		return nil, errors.New("未能读取完整剪贴板数据")
	}
	return out, nil
}

func nativeClipboard(ctx context.Context, digest string) ([]byte, string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Clipboard ownership is per thread. Compare the browser paste event's text
	// while the very same clipboard is open before fetching the embedded package.
	for attempt := 0; ; attempt++ {
		if ok, _, _ := openClipboard.Call(0); ok != 0 {
			break
		}
		if attempt >= 12 {
			return nil, "", errors.New("剪贴板正忙，请重试")
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
	defer closeClipboard.Call()
	embed, xml := clipFormat("Embed Source"), clipFormat("XML Spreadsheet")
	if ok, _, _ := clipboardAvailable.Call(xml); ok == 0 {
		return nil, "", nil
	}
	if ok, _, _ := clipboardAvailable.Call(embed); ok == 0 {
		return nil, "", errors.New("Excel 只提供了显示结果。可用 Ctrl+Shift+V 粘贴文字")
	}
	raw, err := clipboardBytes(13, formats.EditRequestBytes*2+2)
	if err != nil {
		return nil, "", err
	}
	if len(raw)%2 != 0 {
		return nil, "", errors.New("剪贴板文本无效")
	}
	units := unsafe.Slice((*uint16)(unsafe.Pointer(&raw[0])), len(raw)/2)
	text := strings.ReplaceAll(syscall.UTF16ToString(units), "\r\n", "\n")
	sum := sha256.Sum256([]byte(strings.TrimSuffix(text, "\n")))
	if hex.EncodeToString(sum[:]) != digest {
		return nil, "", errors.New("剪贴板内容已变化，请重新粘贴")
	}
	// Link carries the sheet of a copied inactive sheet as well. Published
	// Windows ANSI names are converted with the system code page.
	var sheet string
	linkID := clipFormat("Link")
	if available, _, _ := clipboardAvailable.Call(linkID); available != 0 {
		link, e := clipboardBytes(linkID, 64<<10)
		if e == nil {
			fields := bytes.SplitN(link, []byte{0}, 4)
			if len(fields) >= 3 && bytes.EqualFold(fields[0], []byte("Excel")) {
				if pos := bytes.LastIndexByte(fields[1], ']'); pos >= 0 && pos+1 < len(fields[1]) {
					name := fields[1][pos+1:]
					buf := make([]uint16, len(name))
					n, _, _ := clipKernel.NewProc("MultiByteToWideChar").Call(0, 0, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
					if n > 0 {
						sheet = string(utf16.Decode(buf[:n]))
					}
				}
			}
		}
	}
	data, err := clipboardBytes(embed, formats.EditClipboardPackageBytes)
	return data, sheet, err
}
