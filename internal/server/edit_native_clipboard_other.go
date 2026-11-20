//go:build !windows

package server

import "context"

func nativeClipboard(context.Context, string) ([]byte, string, error) { return nil, "", nil }
