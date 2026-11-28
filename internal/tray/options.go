package tray

import "excellens/internal/i18n"

// Options contains user-initiated callbacks. Cleanup reports reflect the
// completed operation.
type Options struct {
	Address              string
	Locale               func() i18n.Locale
	LanguageChoice       func() i18n.Locale
	SetLanguageChoice    func(i18n.Locale) error
	OnOpen               func()
	OnChooseFile         func(path string) error
	OnOpenCache          func() error
	OnOpenTemp           func() error
	OnOpenLog            func() error
	OnOpenExports        func() error
	OnOpenNotes          func() error
	OnOpenLayouts        func() error
	OnOpenProgram        func() error
	RibbonLayout         func() (bool, error)
	SetRibbonLayout      func(bool) error
	OnClearAllCaches     func() (string, error)
	OnClearCache         func() (string, error)
	OnClearTemp          func() (string, error)
	ContextMenuInstalled func() bool
	OnInstallContextMenu func() error
	OnRemoveContextMenu  func() error
}

func (o Options) language() i18n.Locale {
	if o.Locale != nil {
		return o.Locale()
	}
	return i18n.Default
}
