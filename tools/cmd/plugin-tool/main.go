package main

import (
	"github.com/ochairo/image-viewport.nvim/tools/supervisor"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ochairo/image-viewport.nvim/tools/devtool"
)

func run(ctx context.Context, args []string) error {
	if len(args) < 2 { return fmt.Errorf("usage: plugin-tool COMMAND ROOT [DESTINATION]") }
	switch args[0] {
	case "source": return devtool.SourceCheck(ctx, args[1])
	case "typecheck": return devtool.Typecheck(ctx, args[1])
	case "fetch":
		if len(args) != 3 { return fmt.Errorf("fetch requires new destination") }
		return devtool.Fetch(ctx, args[1], args[2])
	case "test": return devtool.EditorTests(ctx, args[1], false)
	case "upstream": return devtool.EditorTests(ctx, args[1], true)
	default: return fmt.Errorf("unknown plugin tool command")
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "__worker" { os.Exit(supervisor.Worker(os.Args[2:])) }
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
}
