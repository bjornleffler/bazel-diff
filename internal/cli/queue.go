// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// The queue command: select targets for several pull requests at once.

package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/bjornleffler/bazel-diff/internal/classify"
	"github.com/bjornleffler/bazel-diff/internal/diff"
	"github.com/bjornleffler/bazel-diff/internal/report"
	"github.com/bjornleffler/bazel-diff/internal/workspace"
)

// combinedOnly attributes a target that no single pull request impacts on its
// own: only their combination does.
const combinedOnly = "(combination)"

// runQueue merges every pull request into the base, selects targets for the
// merged result, and optionally attributes each selected target to the pull
// requests that impact it on their own.
func runQueue(ctx context.Context, args []string) error {
	fs := newFlagSet("queue", "--base <rev> --pr <rev> [--pr <rev>...] [flags]")
	var rf repoFlags
	var hf hashFlags
	var sf selectFlags
	rf.register(fs)
	hf.register(fs)
	sf.register(fs)
	baseRev := fs.String("base", "", "tip of the branch the pull requests merge into (required)")
	var prs listFlag
	fs.Var(&prs, "pr", "pull request head revision; repeatable (required)")
	perPR := fs.Bool("per-pr", false, "also hash each pull request merged alone, and write attribution.json")
	outDir := fs.String("out-dir", "bazel-diff-out", "directory for the selection files")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *baseRev == "" || len(prs) == 0 {
		return fmt.Errorf("%w: --base and at least one --pr are required", errUsage)
	}
	start := time.Now()

	s, err := newSnapshotter(ctx, &rf, hf.config())
	if err != nil {
		return err
	}
	baseCommit, err := s.resolve(ctx, *baseRev)
	if err != nil {
		return err
	}
	prCommits := make([]string, len(prs))
	for i, pr := range prs {
		if prCommits[i], err = s.resolve(ctx, pr); err != nil {
			return err
		}
	}

	base, err := s.atCommit(ctx, "base", baseCommit, outPath(*outDir, report.BaseSnapshotFile))
	if err != nil {
		return err
	}
	dir, mergeCommit, err := s.worktrees.Merge(ctx, "merge", baseCommit, prCommits)
	if err != nil {
		return err
	}
	logf("merged %d pull requests into %s as %s", len(prs), short(baseCommit), short(mergeCommit))
	head, err := s.hashDir(ctx, dir, outPath(*outDir, report.HeadSnapshotFile), false)
	if err != nil {
		return err
	}
	if err := writeSelection(*outDir, base, head, sf.options(), start); err != nil {
		return err
	}
	if !*perPR {
		return nil
	}

	// Attribute each target of the combined selection to the pull requests
	// whose own selection contains it.
	combined := classify.Select(diff.Compare(base, head), sf.options())
	attribution := make(map[string][]string)
	for i, pr := range prs {
		dir, commit, err := s.worktrees.Merge(ctx, "merge", baseCommit, []string{prCommits[i]})
		if err != nil {
			return err
		}
		prDir := filepath.Join(*outDir, "prs", workspace.SafeName(pr))
		logf("hashing %s alone (merge %s)", pr, short(commit))
		single, err := s.hashDir(ctx, dir, outPath(prDir, report.HeadSnapshotFile), false)
		if err != nil {
			return err
		}
		if err := writeSelection(prDir, base, single, sf.options(), start); err != nil {
			return err
		}
		sel := classify.Select(diff.Compare(base, single), sf.options())
		for _, l := range append(sel.Build, sel.Tests...) {
			attribution[l] = append(attribution[l], pr)
		}
	}
	out := make(map[string][]string)
	for _, l := range append(combined.Build, combined.Tests...) {
		owners := attribution[l]
		if len(owners) == 0 {
			owners = []string{combinedOnly}
		}
		sort.Strings(owners)
		out[l] = owners
	}
	return report.WriteJSON(outPath(*outDir, report.AttributionFile), out)
}
