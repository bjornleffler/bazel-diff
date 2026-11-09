// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// The impacted command: select targets for one change.

package cli

import (
	"context"
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

// runImpacted compares a base revision with the head of a change and writes
// the build and test selection.
func runImpacted(ctx context.Context, args []string) error {
	fs := newFlagSet("impacted", "--base <rev> [--head <rev>] [flags]")
	var rf repoFlags
	var hf hashFlags
	var sf selectFlags
	rf.register(fs)
	hf.register(fs)
	sf.register(fs)
	baseRev := fs.String("base", "", "revision the change is compared against, usually the target branch (required)")
	headRev := fs.String("head", "", "head revision; default: the working tree as it is on disk")
	noMergeBase := fs.Bool("no-merge-base", false, "compare with --base itself instead of its merge base with head")
	parallel := fs.Bool("parallel", false, "on a cache miss, hash base and head at the same time (two Bazel servers)")
	outDir := fs.String("out-dir", "bazel-diff-out", "directory for the selection files")
	var fastPatterns listFlag
	fs.Var(&fastPatterns, "fast-path", "glob of files whose modification alone never needs a build, e.g. '**/*.md'; repeatable")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *baseRev == "" {
		return fmt.Errorf("%w: --base is required", errUsage)
	}
	start := time.Now()

	s, err := newSnapshotter(ctx, &rf, hf.config())
	if err != nil {
		return err
	}
	inPlace := *headRev == ""
	headCommit, err := s.resolve(ctx, "HEAD")
	if !inPlace {
		headCommit, err = s.resolve(ctx, *headRev)
	}
	if err != nil {
		return err
	}
	baseCommit, err := s.resolve(ctx, *baseRev)
	if err != nil {
		return err
	}
	if !*noMergeBase {
		if baseCommit, err = s.repo.MergeBase(ctx, baseCommit, headCommit); err != nil {
			return err
		}
	}
	logf("base %s, head %s%s", short(baseCommit), short(headCommit), map[bool]string{true: " (working tree)"}[inPlace])

	basePath := outPath(*outDir, report.BaseSnapshotFile)
	headPath := outPath(*outDir, report.HeadSnapshotFile)
	base := s.fromCache(ctx, baseCommit, basePath)

	if base != nil && len(fastPatterns) > 0 {
		skip, reason, err := checkFastPath(ctx, s, base, baseCommit, headCommit, inPlace, fastPatterns)
		if err != nil {
			return err
		}
		logf("fast path: %s", reason)
		if skip {
			result := &diff.Result{BaseCommit: baseCommit, HeadCommit: headCommit}
			return report.Write(*outDir, result, classify.Select(result, sf.options()),
				report.Options{FastPath: true, DurationMs: time.Since(start).Milliseconds()})
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
	if base == nil && *parallel {
		base, head, err = both(hashBase, hashHead)
	} else {
		if base == nil {
			if base, err = hashBase(); err != nil {
				return err
			}
		}
		head, err = hashHead()
	}
	if err != nil {
		return err
	}
	return writeSelection(*outDir, base, head, sf.options(), start)
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
// files and logs a one-line summary.
func writeSelection(outDir string, base, head *snapshot.Snapshot, opts classify.Options, start time.Time) error {
	result := diff.Compare(base, head)
	sel := classify.Select(result, opts)
	if err := report.Write(outDir, result, sel, report.Options{DurationMs: time.Since(start).Milliseconds()}); err != nil {
		return err
	}
	for _, reason := range result.Reasons {
		logf("full build: %s", reason)
	}
	logf("%d impacted targets: %d to build, %d tests, %d deferred tests; wrote %s",
		len(result.Impacted), len(sel.Build), len(sel.Tests), len(sel.DeferredTests), outDir)
	return nil
}
