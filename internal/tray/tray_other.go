//go:build !windows

package tray

import "context"

type Options struct {
	Address              string
	OnOpen               func()
	OnChooseFile         func(string) error
	OnOpenCache          func() error
	OnOpenTemp           func() error
	OnOpenLog            func() error
	OnOpenExports        func() error
	OnOpenNotes          func() error
	OnOpenLayouts        func() error
	OnClearCache         func() (string, error)
	OnClearTemp          func() (string, error)
	ContextMenuInstalled func() bool
	OnInstallContextMenu func() error
	OnRemoveContextMenu  func() error
}

func Run(ctx context.Context, options Options) error { <-ctx.Done(); return nil }
func RequestQuit()                                   {}
