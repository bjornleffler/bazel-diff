// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Command bazel-diff2 selects the Bazel targets that a change, or a set of
// changes, needs built and tested.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/bjornleffler/bazel-diff2/internal/cli"
)

// main runs the command line, cancelling child processes on SIGINT/SIGTERM.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
