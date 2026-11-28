//go:build !windows

package tray

import "context"

func Run(ctx context.Context, options Options) error { <-ctx.Done(); return nil }
func RequestQuit()                                   {}
