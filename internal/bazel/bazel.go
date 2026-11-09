// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Package bazel runs `bazel info` and `bazel query` in a workspace and streams
// the query result as protocol buffers.
package bazel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/bjornleffler/bazel-diff/internal/buildpb"
	"google.golang.org/protobuf/encoding/protodelim"
)

// queryExitPartial is Bazel's exit code for a --keep_going query that skipped
// packages it could not load.
const queryExitPartial = 3

// QueryFlags are always passed to `bazel query`. --consistent_labels prints
// canonical labels ("@@repo+//pkg:name") that do not depend on repository
// mappings, and --noproto:flatten_selects keeps select() structure, so a change
// to which branch holds a value still changes the rule's hash.
var QueryFlags = []string{
	"--output=streamed_proto",
	"--consistent_labels",
	"--noproto:flatten_selects",
	"--order_output=no",
	"--tool_tag=bazel-diff",
}

// Runner invokes Bazel in one workspace.
type Runner struct {
	// Bazel is the binary to run; "bazel" (normally bazelisk) when empty.
	Bazel string
	// StartupFlags go before the command, such as --output_base.
	StartupFlags []string
	// Dir is the workspace directory.
	Dir string
	// Stderr receives Bazel's progress output; os.Stderr when nil.
	Stderr io.Writer
}

// command builds an exec.Cmd for one Bazel command.
func (r Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	bin := r.Bazel
	if bin == "" {
		bin = "bazel"
	}
	full := append(append([]string{}, r.StartupFlags...), args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = r.Dir
	cmd.Stderr = r.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	return cmd
}

// Info returns the values of the given `bazel info` keys, such as
// "output_base" or "release", in one invocation.
func (r Runner) Info(ctx context.Context, keys ...string) (map[string]string, error) {
	cmd := r.command(ctx, append([]string{"info"}, keys...)...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("bazel info in %s: %w", r.Dir, err)
	}
	values := make(map[string]string)
	for _, line := range strings.Split(stdout.String(), "\n") {
		if key, value, ok := strings.Cut(line, ": "); ok {
			values[key] = strings.TrimSpace(value)
		}
	}
	// With a single key Bazel prints the bare value, without "key: ".
	if len(keys) == 1 && len(values) == 0 {
		values[keys[0]] = strings.TrimSpace(stdout.String())
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return nil, fmt.Errorf("bazel info printed no %q", key)
		}
	}
	return values, nil
}

// Query runs `bazel query expr` and calls fn for every target in the result.
// extraFlags follow QueryFlags. With keepGoing, a partial result (exit code 3)
// is accepted; otherwise any failure is an error, because a target missing
// from the result would look deleted and so never be built.
func (r Runner) Query(ctx context.Context, expr string, extraFlags []string, keepGoing bool, fn func(*buildpb.Target) error) error {
	args := append([]string{"query"}, QueryFlags...)
	args = append(args, extraFlags...)
	if keepGoing {
		args = append(args, "--keep_going")
	}
	args = append(args, "--", expr)
	cmd := r.command(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting bazel query: %w", err)
	}

	readErr := readTargets(bufio.NewReaderSize(stdout, 1<<20), fn)
	if readErr != nil {
		// Unblock Bazel if we stopped reading early.
		io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) && keepGoing && exitErr.ExitCode() == queryExitPartial {
		fmt.Fprintln(cmd.Stderr, "bazel-diff: warning: bazel query returned a partial result (--keep_going)")
		return nil
	}
	if waitErr != nil {
		return fmt.Errorf("bazel query %q in %s: %w", expr, r.Dir, waitErr)
	}
	return nil
}

// readTargets decodes length-delimited Target messages until end of input.
func readTargets(r *bufio.Reader, fn func(*buildpb.Target) error) error {
	opts := protodelim.UnmarshalOptions{MaxSize: -1}
	for {
		t := &buildpb.Target{}
		err := opts.UnmarshalFrom(r, t)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("decoding bazel query output: %w", err)
		}
		if err := fn(t); err != nil {
			return err
		}
	}
}
