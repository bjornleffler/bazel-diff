// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// The impacted command: select targets for one change.

package cli

import (
	"context"
	"flag"
	"fmt"
	"sync"
	"time"

	"github.com/bjornleffler/bazel-diff/internal/classify"
	"github.com/bjornleffler/bazel-diff/internal/diff"
	"github.com/bjornleffler/bazel-diff/internal/fastpath"
	"github.com/bjornleffler/bazel-diff/internal/glob"
	"github.com/bjornleffler/bazel-diff/internal/report"
	"github.com/bjornleffler/bazel-diff/internal/snapshot"
)

// changeFlags describe one change: the revisions to compare and how to turn
// the difference into a selection. The impacted and build commands share them.
type changeFlags struct {
	rf           repoFlags
	hf           hashFlags
	sf           selectFlags
	baseRev      string
	headRev      string
	noMergeBase  bool
	parallel     bool
	outDir       string
	fastPatterns listFlag
}

// register adds the change flags to fs. defaultBase is the --base default;
// empty makes --base required.
func (c *changeFlags) register(fs *flag.FlagSet, defaultBase string) {
	c.rf.register(fs)
	c.hf.register(fs)
	c.sf.register(fs)
	baseHelp := "revision the change is compared against, usually the target branch (required)"
	if defaultBase != "" {
		baseHelp = "revision the change is compared against"
	}
	fs.StringVar(&c.baseRev, "base", defaultBase, baseHelp)
	fs.StringVar(&c.headRev, "head", "", "head revision; default: the working tree as it is on disk")
	fs.BoolVar(&c.noMergeBase, "no-merge-base", false, "compare with --base itself instead of its merge base with head")
	fs.BoolVar(&c.parallel, "parallel", false, "on a cache miss, hash base and head at the same time (two Bazel servers)")
	fs.StringVar(&c.outDir, "out-dir", "bazel-diff-out", "directory for the selection files")
	fs.Var(&c.fastPatterns, "fast-path", "glob of files whose modification alone never needs a build, e.g. '**/*.md'; repeatable")
}

// runImpacted compares a base revision with the head of a change and writes
// the build and test selection.
func runImpacted(ctx context.Context, args []string) error {
	fs := newFlagSet("impacted", "--base <rev> [--head <rev>] [flags]")
	var cf changeFlags
	cf.register(fs, "")
	if err := parse(fs, args); err != nil {
		return err
	}
	if cf.baseRev == "" {
		return fmt.Errorf("%w: --base is required", errUsage)
	}
	_, err := cf.selectChange(ctx)
	return err
}

// selectChange hashes base and head (or reuses cached snapshots), writes the
// selection files to the output directory and returns the selection.
func (c *changeFlags) selectChange(ctx context.Context) (classify.Selection, error) {
	start := time.Now()
	s, err := newSnapshotter(ctx, &c.rf, c.hf.config())
	if err != nil {
		return classify.Selection{}, err
	}
	inPlace := c.headRev == ""
	headCommit, err := s.resolve(ctx, "HEAD")
	if !inPlace {
		headCommit, err = s.resolve(ctx, c.headRev)
	}
	if err != nil {
		return classify.Selection{}, err
	}
	baseCommit, err := s.resolve(ctx, c.baseRev)
	if err != nil {
		return classify.Selection{}, err
	}
	if !c.noMergeBase {
		if baseCommit, err = s.repo.MergeBase(ctx, baseCommit, headCommit); err != nil {
			return classify.Selection{}, err
		}
	}
	logf("base %s, head %s%s", short(baseCommit), short(headCommit), map[bool]string{true: " (working tree)"}[inPlace])

	basePath := outPath(c.outDir, report.BaseSnapshotFile)
	headPath := outPath(c.outDir, report.HeadSnapshotFile)
	base := s.fromCache(ctx, baseCommit, basePath)

	if base != nil && len(c.fastPatterns) > 0 {
		skip, reason, err := checkFastPath(ctx, s, base, baseCommit, headCommit, inPlace, c.fastPatterns)
		if err != nil {
			return classify.Selection{}, err
		}
		logf("fast path: %s", reason)
		if skip {
			result := &diff.Result{BaseCommit: baseCommit, HeadCommit: headCommit}
			sel := classify.Select(result, c.sf.options())
			err := report.Write(c.outDir, result, sel,
				report.Options{FastPath: true, DurationMs: time.Since(start).Milliseconds()})
			return sel, err
		}
	}

	hashHead := func() (*snapshot.Snapshot, error) {
		if inPlace {
			return s.inPlace(ctx, headPath)
		}
		return s.atCommit(ctx, "head", headCommit, headPath)
	}
	hashBase := func() (*snapshot.Snapshot, error) {
		// The cache was already consulted above.
		return s.computeAt(ctx, "base", baseCommit, basePath)
	}
	var head *snapshot.Snapshot
	if base == nil && c.parallel {
		base, head, err = both(hashBase, hashHead)
	} else {
		if base == nil {
			if base, err = hashBase(); err != nil {
				return classify.Selection{}, err
			}
		}
		head, err = hashHead()
	}
	if err != nil {
		return classify.Selection{}, err
	}
	return writeSelection(c.outDir, base, head, c.sf.options(), start)
}

// checkFastPath decides whether the change between two commits can skip the
// head query. Local changes in an in-place head always disable it, since the
// commit-to-commit file list would not see them.
func checkFastPath(ctx context.Context, s *snapshotter, base *snapshot.Snapshot, baseCommit, headCommit string, inPlace bool, patterns []string) (bool, string, error) {
	allow, err := glob.CompileAll(patterns)
	if err != nil {
		return false, "", fmt.Errorf("%w: %v", errUsage, err)
	}
	seeds, err := glob.CompileAll(s.cfg.Seeds)
	if err != nil {
		return false, "", fmt.Errorf("%w: %v", errUsage, err)
	}
	if inPlace {
		dirty, err := s.repo.IsDirty(ctx)
		if err != nil {
			return false, "", err
		}
		if dirty {
			return false, "the working tree has local changes", nil
		}
	}
	changes, err := s.repo.ChangedFiles(ctx, baseCommit, headCommit)
	if err != nil {
		return false, "", err
	}
	skip, reason := fastpath.Check(changes, base, allow, seeds)
	return skip, reason, nil
}

// both runs two snapshot functions concurrently.
func both(a, b func() (*snapshot.Snapshot, error)) (*snapshot.Snapshot, *snapshot.Snapshot, error) {
	var wg sync.WaitGroup
	var sa, sb *snapshot.Snapshot
	var ea, eb error
	wg.Add(2)
	go func() { defer wg.Done(); sa, ea = a() }()
	go func() { defer wg.Done(); sb, eb = b() }()
	wg.Wait()
	if ea != nil {
		return nil, nil, ea
	}
	return sa, sb, eb
}

// writeSelection diffs two snapshots, selects targets, writes the output
// files, logs a one-line summary and returns the selection.
func writeSelection(outDir string, base, head *snapshot.Snapshot, opts classify.Options, start time.Time) (classify.Selection, error) {
	result := diff.Compare(base, head)
	sel := classify.Select(result, opts)
	if err := report.Write(outDir, result, sel, report.Options{DurationMs: time.Since(start).Milliseconds()}); err != nil {
		return sel, err
	}
	for _, reason := range result.Reasons {
		logf("full build: %s", reason)
	}
	logf("%d impacted targets: %d to build, %d tests, %d deferred tests; wrote %s",
		len(result.Impacted), len(sel.Build), len(sel.Tests), len(sel.DeferredTests), outDir)
	return sel, nil
}
