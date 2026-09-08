//go:build linux && (arm64 || amd64)

package main

import (
	"github.com/ochairo/image-viewport.nvim/tools/supervisor"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ochairo/image-viewport.nvim/tools/imageprocessor"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__worker" { os.Exit(supervisor.Worker(os.Args[2:])) }
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if len(os.Args) != 2 { fmt.Fprintln(os.Stderr, "usage: image-build PLUGIN_ROOT"); os.Exit(2) }
	if err := imageprocessor.Build(ctx, os.Args[1]); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}
