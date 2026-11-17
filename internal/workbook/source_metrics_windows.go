//go:build windows

package workbook

import (
	"math"
	"syscall"
	"unsafe"
)

var sourceGDI = syscall.NewLazyDLL("gdi32.dll")
var sourceCreateDC = sourceGDI.NewProc("CreateCompatibleDC")
var sourceDeleteDC = sourceGDI.NewProc("DeleteDC")
var sourceCreateFont = sourceGDI.NewProc("CreateFontW")
var sourceSelectObject = sourceGDI.NewProc("SelectObject")
var sourceDeleteObject = sourceGDI.NewProc("DeleteObject")
var sourceTextExtent = sourceGDI.NewProc("GetTextExtentPoint32W")

// Excel column units use the maximum digit width of the Normal style font,
// measured at 96 DPI. A memory DC reads installed font metrics without opening
// a window or requiring Office/CGO. Layout remains in unzoomed CSS pixels.
func measureSourceDigits(normal VisualStyle) float64 {
	name, points := normal.FontName, normal.FontSize
	if name == "" || !(points > 0 && points < 200) {
		return 7
	}
	face, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 7
	}
	dc, _, _ := sourceCreateDC.Call(0)
	if dc == 0 {
		return 7
	}
	defer sourceDeleteDC.Call(dc)
	height := int32(-math.Round(points * 96 / 72))
	weight, italic := uintptr(400), uintptr(0)
	if normal.Bold {
		weight = 700
	}
	if normal.Italic {
		italic = 1
	}
	font, _, _ := sourceCreateFont.Call(uintptr(height), 0, 0, 0, weight, italic, 0, 0, 1, 0, 0, 0, 0, uintptr(unsafe.Pointer(face)))
	if font == 0 {
		return 7
	}
	defer sourceDeleteObject.Call(font)
	old, _, _ := sourceSelectObject.Call(dc, font)
	if old == 0 || old == ^uintptr(0) {
		return 7
	}
	defer sourceSelectObject.Call(dc, old)
	maximum := int32(0)
	for digit := uint16('0'); digit <= '9'; digit++ {
		var size struct{ X, Y int32 }
		ok, _, _ := sourceTextExtent.Call(dc, uintptr(unsafe.Pointer(&digit)), 1, uintptr(unsafe.Pointer(&size)))
		if ok == 0 {
			return 7
		}
		if size.X > maximum {
			maximum = size.X
		}
	}
	if maximum < 1 || maximum > 100 {
		return 7
	}
	return float64(maximum)
}
