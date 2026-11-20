//go:build !windows

package server

import (
	"context"
	"excellens/internal/editfile"
)

func nativeClipboard(context.Context, string) ([]byte, editfile.NativeSelection, error) {
	return nil, editfile.NativeSelection{}, nil
}
