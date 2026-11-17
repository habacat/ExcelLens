package branding

import "embed"

// The SVG source is the single source of truth for the favicon, tray and EXE
// assets. icon.ico is generated from icon.svg for Windows shell compatibility.
//
//go:embed icon.svg icon.ico
var Files embed.FS

func IconICO() []byte {
	b, _ := Files.ReadFile("icon.ico")
	return b
}
