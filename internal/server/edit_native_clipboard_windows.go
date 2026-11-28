//go:build windows

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"excellens/internal/editfile"
	"excellens/internal/formats"
	"excellens/internal/i18n"
)

var clipUser = syscall.NewLazyDLL("user32.dll")
var clipKernel = syscall.NewLazyDLL("kernel32.dll")
var openClipboard = clipUser.NewProc("OpenClipboard")
var closeClipboard = clipUser.NewProc("CloseClipboard")
var getClipboardData = clipUser.NewProc("GetClipboardData")
var registerClipboardFormat = clipUser.NewProc("RegisterClipboardFormatW")
var clipboardAvailable = clipUser.NewProc("IsClipboardFormatAvailable")
var clipboardSequence = clipUser.NewProc("GetClipboardSequenceNumber")
var clipboardOwner = clipUser.NewProc("GetClipboardOwner")
var clipboardLock = clipKernel.NewProc("GlobalLock")
var clipboardUnlock = clipKernel.NewProc("GlobalUnlock")
var clipboardSize = clipKernel.NewProc("GlobalSize")
var errClipboardNotReady = i18n.Error("service.editNativeClipboardWindows.excelNotYetProvidedClipboard")

func clipFormat(name string) uintptr {
	p, _ := syscall.UTF16PtrFromString(name)
	id, _, _ := registerClipboardFormat.Call(uintptr(unsafe.Pointer(p)))
	return id
}

func clipboardBytes(id uintptr, limit int) ([]byte, error) {
	h, _, cause := getClipboardData.Call(id)
	if h == 0 {
		if cause == syscall.Errno(1418) { // ERROR_CLIPBOARD_NOT_OPEN
			return nil, cause
		}
		return nil, errClipboardNotReady
	}
	n, _, _ := clipboardSize.Call(h)
	if n == 0 || n > uintptr(limit) {
		return nil, errEditRequestCapacity
	}
	ptr, _, _ := clipboardLock.Call(h)
	if ptr == 0 {
		return nil, i18n.Error("service.editNativeClipboardWindows.clipboardBusyTryAgain")
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
		return nil, i18n.Error("service.editNativeClipboardWindows.unableReadCompleteClipboardData")
	}
	return out, nil
}

func openNativeClipboard(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		if ok, _, _ := openClipboard.Call(0); ok != 0 {
			return nil
		}
		if attempt >= 12 {
			return i18n.Error("service.editNativeClipboardWindows.clipboardBusyTryAgain")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
}

func nativeClipboard(ctx context.Context, digest string) ([]byte, editfile.NativeSelection, error) {
	var selection editfile.NativeSelection
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := openNativeClipboard(ctx); err != nil {
		return nil, selection, err
	}
	defer closeClipboard.Call()
	sequence, _, _ := clipboardSequence.Call()
	owner, _, _ := clipboardOwner.Call()
	unchanged := func() bool {
		now, _, _ := clipboardSequence.Call()
		window, _, _ := clipboardOwner.Call()
		return now == sequence && window == owner
	}
	changed := i18n.Error("service.editNativeClipboardWindows.clipboardContentChangedPasteAgain")
	// Excel can release the requesting thread's lock or temporarily return a
	// null handle while rendering a delayed format. Let its owner finish with
	// the clipboard unlocked, then verify identity before retrying that format.
	read := func(id uintptr, limit int) ([]byte, error) {
		for attempt := 0; ; attempt++ {
			data, err := clipboardBytes(id, limit)
			if !errors.Is(err, syscall.Errno(1418)) && !errors.Is(err, errClipboardNotReady) {
				return data, err
			}
			if attempt == 5 {
				return nil, errClipboardNotReady
			}
			closeClipboard.Call()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(1+attempt) * 15 * time.Millisecond):
			}
			if err = openNativeClipboard(ctx); err != nil {
				return nil, err
			}
			if sequence == 0 || !unchanged() {
				return nil, changed
			}
		}
	}
	embed, xmlID := clipFormat("Embed Source"), clipFormat("XML Spreadsheet")
	if ok, _, _ := clipboardAvailable.Call(xmlID); ok == 0 {
		return nil, selection, nil
	}
	if ok, _, _ := clipboardAvailable.Call(embed); ok == 0 {
		return nil, selection, i18n.Error("service.editNativeClipboardWindows.excelOnlyProvidesResultsCan")
	}
	raw, err := read(13, formats.EditRequestBytes*2+2)
	if err != nil {
		return nil, selection, err
	}
	if len(raw)%2 != 0 {
		return nil, selection, i18n.Error("service.editNativeClipboardWindows.clipboardTextInvalid")
	}
	units := unsafe.Slice((*uint16)(unsafe.Pointer(&raw[0])), len(raw)/2)
	text := strings.ReplaceAll(syscall.UTF16ToString(units), "\r\n", "\n")
	sum := sha256.Sum256([]byte(strings.TrimSuffix(text, "\n")))
	if hex.EncodeToString(sum[:]) != digest {
		return nil, selection, i18n.Error("service.editNativeClipboardWindows.clipboardContentChangedPasteAgain")
	}
	// Link carries the sheet of a copied inactive sheet as well. Published
	// Windows ANSI names are converted with the system code page.
	var sheet string
	var fieldsRange []byte
	linkID := clipFormat("Link")
	if available, _, _ := clipboardAvailable.Call(linkID); available != 0 {
		link, e := read(linkID, 64<<10)
		if errors.Is(e, changed) {
			return nil, selection, e
		}
		if e == nil {
			fields := bytes.SplitN(link, []byte{0}, 4)
			if len(fields) >= 3 && bytes.EqualFold(fields[0], []byte("Excel")) {
				fieldsRange = fields[2]
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
	selection.Sheet = sheet
	rawXML, e := read(xmlID, formats.EditRequestBytes*4)
	if e != nil {
		return nil, selection, e
	}
	h, w, e := clipboardGeometry(rawXML)
	if e != nil {
		return nil, selection, e
	}
	selection.VisibleRows, selection.VisibleCols = h, w
	data, err := read(embed, formats.EditClipboardPackageBytes)
	if err != nil {
		return nil, selection, err
	}
	// Most selections need only OOXML. Fetch BIFF8 if the public clipboard
	// rectangle differs in size from the declared source range.
	if !clipboardDimensionsMatch(fieldsRange, h, w) {
		biff := clipFormat("Biff8")
		if ok, _, _ := clipboardAvailable.Call(biff); ok != 0 {
			// Only a compacted, filtered source requires these coordinates.
			// The range reader checks that requirement against the workbook.
			selection.RowRecords, _ = read(biff, formats.EditClipboardPackageBytes)
		}
		shadow := clipFormat("ShadowWorkbook")
		if ok, _, _ := clipboardAvailable.Call(shadow); ok != 0 {
			// Optional corroboration for hidden cells. Older Excel builds and
			// large snapshots still work with an explicit selection.
			selection.Shadow, _ = read(shadow, 2*formats.EditRequestBytes)
			if ctx.Err() != nil {
				return nil, selection, ctx.Err()
			}
		}
	}
	if !unchanged() {
		return nil, selection, changed
	}
	return data, selection, ctx.Err()
}

func clipboardGeometry(raw []byte) (int, int, error) {
	decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimRight(raw, "\x00")))
	for {
		t, err := decoder.Token()
		if err != nil {
			return 0, 0, err
		}
		el, ok := t.(xml.StartElement)
		if !ok || el.Name != (xml.Name{Space: "urn:schemas-microsoft-com:office:spreadsheet", Local: "Table"}) {
			continue
		}
		var h, w int
		for _, at := range el.Attr {
			switch at.Name.Local {
			case "ExpandedRowCount":
				h, _ = strconv.Atoi(at.Value)
			case "ExpandedColumnCount":
				w, _ = strconv.Atoi(at.Value)
			}
		}
		return h, w, nil
	}
}

var nativeBounds = regexp.MustCompile(`^R([0-9]+)C([0-9]+)(?::R([0-9]+)C([0-9]+))?$`)

func clipboardDimensionsMatch(raw []byte, h, w int) bool {
	p := nativeBounds.FindSubmatch(raw)
	if p == nil {
		return false
	}
	n := func(x []byte) int { v, _ := strconv.Atoi(string(x)); return v }
	if len(p[3]) == 0 {
		return h == 1 && w == 1
	}
	return n(p[3])-n(p[1])+1 == h && n(p[4])-n(p[2])+1 == w
}
