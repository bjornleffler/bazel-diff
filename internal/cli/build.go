// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// The build, test and analyse commands: build, test, or just list, only what
// pending changes affect.

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bjornleffler/bazel-diff/internal/classify"
	"github.com/bjornleffler/bazel-diff/internal/label"
)

// The build and test commands write their targets to these files in the
// output directory and hand them to Bazel's --target_pattern_file, so long
// target lists never hit the command line length limit.
const (
	buildTargetsFile = "build_command_targets.txt"
	testTargetsFile  = "test_command_targets.txt"
)

// bazelExitError carries the exit code of a failed Bazel invocation, which
// Main passes through as its own.
type bazelExitError struct {
	verb string
	code int
}

// Error implements error.
func (e *bazelExitError) Error() string {
	return fmt.Sprintf("bazel %s failed with exit code %d", e.verb, e.code)
}

// runBuild selects the targets that pending changes affect, restricted to
// the given target patterns, prints them and builds them with `bazel build`.
// By default the change is the working tree compared with HEAD.
func runBuild(ctx context.Context, args []string) error {
	fs := newFlagSet("build", "[flags] <target pattern>... [-- <bazel build flags>]")
	var cf changeFlags
	cf.register(fs, "HEAD")
	dryRun := fs.Bool("dry-run", false, "print the affected targets without building them")
	patterns, bazelFlags, err := parseBuildArgs(fs, args)
	if err != nil {
		return err
	}
	sel, err := cf.selectChange(ctx)
	if err != nil {
		return err
	}
	targets := selectAffected(sel, patterns).all()
	if len(targets) == 0 {
		logf("no affected targets match %s; nothing to build", joinPatterns(patterns))
		return nil
	}
	if sel.FullBuild {
		logf("full build: building %s", joinPatterns(patterns))
	} else {
		logf("%d affected targets match %s:", len(targets), joinPatterns(patterns))
	}
	printTargets(targets)
	if *dryRun {
		return nil
	}

	return runBazel(ctx, &cf, "build", bazelFlags, buildTargetsFile, targets)
}

// runTest selects the targets that pending changes affect, restricted to the
// given target patterns, prints them, then builds them and runs the affected
// tests with `bazel test`. When no tests are affected it runs `bazel build`
// instead, since `bazel test` fails when it finds no tests.
func runTest(ctx context.Context, args []string) error {
	fs := newFlagSet("test", "[flags] <target pattern>... [-- <bazel test flags>]")
	var cf changeFlags
	cf.register(fs, "HEAD")
	patterns, bazelFlags, err := parseBuildArgs(fs, args)
	if err != nil {
		return err
	}
	sel, err := cf.selectChange(ctx)
	if err != nil {
		return err
	}
	a := selectAffected(sel, patterns)
	if a.fullBuild {
		logf("full build: testing %s", joinPatterns(patterns))
		printTargets(a.build)
		return runBazel(ctx, &cf, "test", bazelFlags, testTargetsFile, a.build)
	}
	if len(a.build) == 0 && len(a.tests) == 0 {
		logf("no affected targets match %s; nothing to build or test", joinPatterns(patterns))
		return nil
	}
	printAffected(a)
	if len(a.deferred) > 0 {
		// `bazel test` runs every test it builds, so deferred tests are left out.
		logf("leaving out %d deferred tests (beyond --max-test-distance)", len(a.deferred))
	}
	if len(a.tests) == 0 {
		logf("no affected tests; building only")
		return runBazel(ctx, &cf, "build", bazelFlags, buildTargetsFile, a.build)
	}
	targets := append(append([]string{}, a.build...), a.tests...)
	sort.Strings(targets)
	return runBazel(ctx, &cf, "test", bazelFlags, testTargetsFile, targets)
}

// runAnalyse is a no-op version of build: it prints the targets that pending
// changes affect, grouped into those that would be built and those that would
// be tested, and runs nothing else.
func runAnalyse(ctx context.Context, args []string) error {
	fs := newFlagSet("analyse", "[flags] <target pattern>...")
	var cf changeFlags
	cf.register(fs, "HEAD")
	patterns, bazelFlags, err := parseBuildArgs(fs, args)
	if err != nil {
		return err
	}
	if len(bazelFlags) > 0 {
		return fmt.Errorf("%w: analyse runs no Bazel build, so it takes no flags after --", errUsage)
	}
	sel, err := cf.selectChange(ctx)
	if err != nil {
		return err
	}
	a := selectAffected(sel, patterns)
	if a.fullBuild {
		logf("full build: everything in %s would be built and tested", joinPatterns(patterns))
		printTargets(a.build)
		return nil
	}
	if len(a.all()) == 0 {
		logf("no affected targets match %s", joinPatterns(patterns))
		return nil
	}
	printAffected(a)
	if len(a.deferred) > 0 {
		logf("%d deferred tests (beyond --max-test-distance) to build only:", len(a.deferred))
		printTargets(a.deferred)
	}
	return nil
}

// parseBuildArgs parses the flags of build, test or analyse, then separates
// the positional arguments into target patterns and, after "--", flags for
// Bazel. At least one pattern is required.
func parseBuildArgs(fs *flag.FlagSet, args []string) ([]label.Pattern, []string, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("%w: %v", errUsage, err)
	}
	args = fs.Args()
	var bazelFlags []string
	for i, a := range args {
		if a == "--" {
			args, bazelFlags = args[:i], args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return nil, nil, fmt.Errorf("%w: give at least one target pattern, such as //...", errUsage)
	}
	var patterns []label.Pattern
	for _, a := range args {
		p, err := label.ParsePattern(a)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", errUsage, err)
		}
		patterns = append(patterns, p)
	}
	return patterns, bazelFlags, nil
}

// affected is a selection restricted to the requested target patterns.
type affected struct {
	fullBuild bool
	// build holds non-test rules, or on a full build the patterns themselves.
	build    []string
	tests    []string
	deferred []string
}

// selectAffected restricts a selection to the targets the patterns cover,
// each group sorted. On a full build the patterns are kept as given.
func selectAffected(sel classify.Selection, patterns []label.Pattern) affected {
	if sel.FullBuild {
		a := affected{fullBuild: true}
		for _, p := range patterns {
			a.build = append(a.build, p.String())
		}
		return a
	}
	filter := func(labels []string) []string {
		out := label.Filter(labels, patterns)
		sort.Strings(out)
		return out
	}
	return affected{build: filter(sel.Build), tests: filter(sel.Tests), deferred: filter(sel.DeferredTests)}
}

// all returns every target that `bazel build` should get: rules and tests
// (`bazel build //...` builds tests too), sorted.
func (a affected) all() []string {
	out := append(append(append([]string{}, a.build...), a.tests...), a.deferred...)
	if !a.fullBuild {
		sort.Strings(out)
	}
	return out
}

// printAffected prints the targets to build, then the tests to run, each
// group under a heading on standard error.
func printAffected(a affected) {
	logf("%d targets to build:", len(a.build))
	printTargets(a.build)
	logf("%d tests to build and run:", len(a.tests))
	printTargets(a.tests)
}

// printTargets writes targets to standard output, one per line.
func printTargets(targets []string) {
	for _, t := range targets {
		fmt.Println(t)
	}
}

// runBazel writes targets to the file called name in the output directory
// and runs `bazel <verb>` (build or test) on it, with Bazel's output going
// straight to the terminal.
func runBazel(ctx context.Context, cf *changeFlags, verb string, bazelFlags []string, name string, targets []string) error {
	targetsPath, err := filepath.Abs(outPath(cf.outDir, name))
	if err != nil {
		return err
	}
	if err := os.WriteFile(targetsPath, []byte(strings.Join(targets, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	args := append([]string{}, cf.hf.startupFlags...)
	args = append(args, verb)
	args = append(args, bazelFlags...)
	args = append(args, "--target_pattern_file="+targetsPath)
	cmd := exec.CommandContext(ctx, cf.hf.bazel, args...)
	cmd.Dir = cf.rf.workspace
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	logf("running %s %s", cf.hf.bazel, strings.Join(args, " "))
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &bazelExitError{verb: verb, code: exitErr.ExitCode()}
	}
	return err
}

// joinPatterns formats patterns for log messages.
func joinPatterns(patterns []label.Pattern) string {
	texts := make([]string, len(patterns))
	for i, p := range patterns {
		texts[i] = p.String()
	}
	return strings.Join(texts, " ")
}
