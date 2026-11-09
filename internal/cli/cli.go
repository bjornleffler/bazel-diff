// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package cli implements the bazel-diff command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bjornleffler/bazel-diff/internal/classify"
	"github.com/bjornleffler/bazel-diff/internal/workspace"
)

// usage is printed for -h and for unknown commands.
const usage = `bazel-diff selects the Bazel targets a change needs built and tested.

Usage: bazel-diff <command> [flags]

Commands:
  analyse   print the targets that pending changes affect, without building, e.g. analyse //...
            (also accepted as analyze)
  build     print and build the targets that pending changes affect, e.g. build //...
  test      print, build and test the targets that pending changes affect, e.g. test //...
  impacted  select targets for one change (a pull request); the usual CI entry point
  queue     select targets for several pull requests merged together
  hash      hash one revision into a snapshot, optionally uploading it to the cache
  diff      compare two snapshot files and write the selection
  explain   show why a target was selected
  verify    shadow mode: check a full run's failures against a selection
  version   print the version

Run 'bazel-diff <command> -h' for a command's flags.
`

// errUsage marks errors caused by bad arguments; Main exits 2 for them.
var errUsage = errors.New("usage error")

// command is one subcommand.
type command struct {
	run func(ctx context.Context, args []string) error
}

// commands maps names to subcommands.
var commands = map[string]command{
	"analyse":  {runAnalyse},
	"analyze":  {runAnalyse}, // the American spelling of analyse
	"build":    {runBuild},
	"test":     {runTest},
	"impacted": {runImpacted},
	"queue":    {runQueue},
	"hash":     {runHash},
	"diff":     {runDiff},
	"explain":  {runExplain},
	"verify":   {runVerify},
	"version":  {runVersion},
}

// Main runs the command line and returns the process exit code.
func Main(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "bazel-diff: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	err := cmd.run(ctx, args[1:])
	var bazelErr *bazelExitError
	switch {
	case err == nil:
		return 0
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUsage):
		fmt.Fprintf(os.Stderr, "bazel-diff %s: %v\n", args[0], err)
		return 2
	case errors.As(err, &bazelErr):
		fmt.Fprintf(os.Stderr, "bazel-diff %s: %v\n", args[0], err)
		return bazelErr.code
	case errors.Is(err, errMisses):
		fmt.Fprintf(os.Stderr, "bazel-diff %s: %v\n", args[0], err)
		return 3
	default:
		fmt.Fprintf(os.Stderr, "bazel-diff %s: error: %v\n", args[0], err)
		return 1
	}
}

// logf prints one progress line to standard error.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bazel-diff: "+format+"\n", args...)
}

// listFlag collects a repeatable string flag.
type listFlag []string

// String implements flag.Value.
func (l *listFlag) String() string { return strings.Join(*l, ",") }

// Set implements flag.Value.
func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// newFlagSet returns a FlagSet that reports errors instead of exiting.
func newFlagSet(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: bazel-diff %s %s\n\nFlags:\n", name, synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parse parses flags and rejects stray positional arguments.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected arguments %q", errUsage, fs.Args())
	}
	return nil
}

// hashFlags are the flags that shape a snapshot. Base and head must agree on
// them, which the snapshot fingerprint checks.
type hashFlags struct {
	bazel            string
	startupFlags     listFlag
	query            string
	queryFlags       listFlag
	keepGoing        bool
	seeds            listFlag
	noDefaultSeeds   bool
	repoMarkerIgnore listFlag
}

// register adds the hash flags to fs.
func (h *hashFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&h.bazel, "bazel", "bazel", "Bazel binary (normally bazelisk)")
	fs.Var(&h.startupFlags, "startup-flag", "Bazel startup flag, such as --output_user_root=...; repeatable")
	fs.StringVar(&h.query, "query", workspace.DefaultQuery, "query expression whose result is hashed")
	fs.Var(&h.queryFlags, "query-flag", "extra flag for `bazel query`; repeatable")
	fs.BoolVar(&h.keepGoing, "keep-going", false, "accept a partial query result; packages that fail to load then look deleted")
	fs.Var(&h.seeds, "seed", "glob of extra seed files whose change forces a full build; repeatable")
	fs.BoolVar(&h.noDefaultSeeds, "no-default-seeds", false, "drop the default seeds ("+strings.Join(workspace.DefaultSeeds, " ")+")")
	fs.Var(&h.repoMarkerIgnore, "repo-marker-ignore", "ignore external repository marker lines with this prefix, e.g. ENV:; repeatable")
}

// config turns the hash flags into a workspace.Config.
func (h *hashFlags) config() workspace.Config {
	cfg := workspace.DefaultConfig()
	cfg.Bazel = h.bazel
	cfg.StartupFlags = h.startupFlags
	cfg.Query = h.query
	cfg.QueryFlags = h.queryFlags
	cfg.KeepGoing = h.keepGoing
	if h.noDefaultSeeds {
		cfg.Seeds = nil
	}
	cfg.Seeds = append(cfg.Seeds, h.seeds...)
	cfg.RepoMarkerIgnore = h.repoMarkerIgnore
	return cfg
}

// selectFlags shape how impacted targets become a build and test list.
type selectFlags struct {
	includeManual   bool
	buildTagFilters string
	testTagFilters  string
	maxTestDistance int
}

// register adds the selection flags to fs.
func (s *selectFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&s.includeManual, "include-manual", false, "keep targets tagged manual")
	fs.StringVar(&s.buildTagFilters, "build-tag-filters", "", "same value as Bazel's --build_tag_filters")
	fs.StringVar(&s.testTagFilters, "test-tag-filters", "", "same value as Bazel's --test_tag_filters")
	fs.IntVar(&s.maxTestDistance, "max-test-distance", -1, "defer tests more than this many dependency edges from a change; -1 for no limit")
}

// options turns the selection flags into classify.Options.
func (s *selectFlags) options() classify.Options {
	return classify.Options{
		IncludeManual:   s.includeManual,
		BuildTagFilters: classify.ParseTagFilters(s.buildTagFilters),
		TestTagFilters:  classify.ParseTagFilters(s.testTagFilters),
		MaxTestDistance: s.maxTestDistance,
	}
}

// runVersion prints the tool version.
func runVersion(ctx context.Context, args []string) error {
	fs := newFlagSet("version", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	_, err := io.WriteString(os.Stdout, "bazel-diff "+workspace.ToolVersion+"\n")
	return err
}
