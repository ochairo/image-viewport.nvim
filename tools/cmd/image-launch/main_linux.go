//go:build linux && (arm64 || amd64)

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ochairo/image-viewport.nvim/tools/imageprocessor"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__anchor" {
		os.Exit(imageprocessor.Anchor(os.Args[2:]))
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	code, err := imageprocessor.Launch(ctx, os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "image processor:", err)
		if code == 0 {
			code = 70
		}
	}
	if ctx.Err() != nil {
		code = 75
	}
	os.Exit(code)
}
