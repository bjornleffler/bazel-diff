// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Command bazel-diff selects the Bazel targets that a change, or a set of
// changes, needs built and tested.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bjornleffler/bazel-diff/internal/cli"
)

// main runs the command line, cancelling child processes on SIGINT/SIGTERM.
func main() {
	// `bazel run` starts binaries in their runfiles directory, inside Bazel's
	// output tree. Move back to where the user ran it, so that the default
	// --workspace and relative paths such as --out-dir mean what they expect.
	if dir := os.Getenv("BUILD_WORKING_DIRECTORY"); dir != "" {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintf(os.Stderr, "bazel-diff: %v\n", err)
			os.Exit(1)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
