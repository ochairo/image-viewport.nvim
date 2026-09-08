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
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := imageprocessor.Worker(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "image worker:", err)
		os.Exit(70)
	}
}
