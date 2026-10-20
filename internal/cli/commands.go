// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// The hash, diff, explain and verify commands.

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/bjornleffler/bazel-diff2/internal/bep"
	"github.com/bjornleffler/bazel-diff2/internal/classify"
	"github.com/bjornleffler/bazel-diff2/internal/diff"
	"github.com/bjornleffler/bazel-diff2/internal/label"
	"github.com/bjornleffler/bazel-diff2/internal/report"
	"github.com/bjornleffler/bazel-diff2/internal/snapshot"
)

// errMisses is returned by verify --fail-on-miss when a full run failed on a
// target the selection left out; Main exits 3 for it.
var errMisses = errors.New("the selection missed failing targets")

// runHash hashes one revision. On the main branch's post-merge pipeline it
// fills the cache that pull request pipelines read their base from.
func runHash(ctx context.Context, args []string) error {
	fs := newFlagSet("hash", "[--rev <rev>] [--out <file>] [--cache <url> --upload]")
	var rf repoFlags
	var hf hashFlags
	rf.register(fs)
	hf.register(fs)
	rev := fs.String("rev", "", "revision to hash in a worktree; default: the working tree as it is on disk")
	out := fs.String("out", "snapshot.json.gz", "where to write the snapshot")
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := newSnapshotter(ctx, &rf, hf.config())
	if err != nil {
		return err
	}
	if *rev == "" {
		_, err = s.inPlace(ctx, *out)
		return err
	}
	commit, err := s.resolve(ctx, *rev)
	if err != nil {
		return err
	}
	if s.store != nil && s.upload {
		// The point of this run is to fill the cache; skip the work if it is
		// already there.
		if snap := s.fromCache(ctx, commit, *out); snap != nil {
			return nil
		}
	}
	_, err = s.atCommit(ctx, "hash", commit, *out)
	return err
}

// runDiff compares two snapshot files.
func runDiff(ctx context.Context, args []string) error {
	fs := newFlagSet("diff", "--base <file> --head <file> [flags]")
	var sf selectFlags
	sf.register(fs)
	basePath := fs.String("base", "", "base snapshot file (required)")
	headPath := fs.String("head", "", "head snapshot file (required)")
	outDir := fs.String("out-dir", "bazel-diff2-out", "directory for the selection files")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *basePath == "" || *headPath == "" {
		return fmt.Errorf("%w: --base and --head are required", errUsage)
	}
	start := time.Now()
	base, head, err := readPair(*basePath, *headPath)
	if err != nil {
		return err
	}
	return writeSelection(*outDir, base, head, sf.options(), start)
}

// readPair reads a base and a head snapshot.
func readPair(basePath, headPath string) (*snapshot.Snapshot, *snapshot.Snapshot, error) {
	base, err := snapshot.Read(basePath)
	if err != nil {
		return nil, nil, err
	}
	head, err := snapshot.Read(headPath)
	if err != nil {
		return nil, nil, err
	}
	return base, head, nil
}

// runExplain prints the chain of dependencies that connects a selected target
// to the change that impacted it.
func runExplain(ctx context.Context, args []string) error {
	fs := newFlagSet("explain", "--target <label> [--out-dir <dir> | --base <file> --head <file>]")
	outDir := fs.String("out-dir", "bazel-diff2-out", "output directory of an impacted or queue run, which holds both snapshots")
	basePath := fs.String("base", "", "base snapshot file; default: base.json.gz in --out-dir")
	headPath := fs.String("head", "", "head snapshot file; default: head.json.gz in --out-dir")
	var targets listFlag
	fs.Var(&targets, "target", "label to explain; repeatable (required)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("%w: --target is required", errUsage)
	}
	if *basePath == "" {
		*basePath = outPath(*outDir, report.BaseSnapshotFile)
	}
	if *headPath == "" {
		*headPath = outPath(*outDir, report.HeadSnapshotFile)
	}
	base, head, err := readPair(*basePath, *headPath)
	if err != nil {
		return err
	}
	result := diff.Compare(base, head)
	byLabel := make(map[string]*diff.Impacted, len(result.Impacted))
	for i := range result.Impacted {
		byLabel[result.Impacted[i].Label] = &result.Impacted[i]
	}
	for _, reason := range result.Reasons {
		fmt.Printf("full build: %s\n", reason)
	}
	for _, target := range targets {
		t, ok := byLabel[label.Normalize(target)]
		if !ok {
			fmt.Printf("%s\n  not impacted\n\n", target)
			continue
		}
		fmt.Printf("%s (%s)\n", t.Label, t.Kind)
		switch {
		case t.New:
			fmt.Println("  new target")
		case t.Direct:
			fmt.Println("  changed directly: its definition or content changed")
		case t.Distance < 0:
			fmt.Println("  impacted, but no path to a direct change was found (dependency cycle?)")
		default:
			fmt.Printf("  impacted through its dependencies, %d hop(s) from %s\n", t.Distance, t.Cause)
		}
		for hop := t; hop.Via != ""; {
			next := byLabel[hop.Via]
			fmt.Printf("    -> %s (%s)\n", next.Label, next.Kind)
			hop = next
		}
		fmt.Println()
	}
	return nil
}

// runVerify is shadow mode: given the build event file of a full run and the
// selection made for the same change, it reports failures the selection
// would have missed.
func runVerify(ctx context.Context, args []string) error {
	fs := newFlagSet("verify", "--bep <file> [--selection <dir>]")
	selDir := fs.String("selection", "bazel-diff2-out", "output directory of the impacted run for the same change")
	bepPath := fs.String("bep", "", "build event JSON file of the full run (--build_event_json_file) (required)")
	failOnMiss := fs.Bool("fail-on-miss", false, "exit with status 3 when the selection missed a failure")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *bepPath == "" {
		return fmt.Errorf("%w: --bep is required", errUsage)
	}
	f, err := os.Open(*bepPath)
	if err != nil {
		return err
	}
	defer f.Close()
	run, err := bep.Parse(f)
	if err != nil {
		return err
	}

	selected := make(map[string]bool)
	deferred := make(map[string]bool)
	everything := false
	for _, name := range []string{report.BuildTargetsFile, report.TestTargetsFile, report.DeferredTestsFile} {
		lines, err := report.ReadLines(outPath(*selDir, name))
		if err != nil {
			return err
		}
		for _, l := range lines {
			if l == classify.Everything {
				everything = true
			}
			if name == report.DeferredTestsFile {
				deferred[label.Normalize(l)] = true
			} else {
				selected[label.Normalize(l)] = true
			}
		}
	}

	type miss struct {
		Label    string `json:"label"`
		Kind     string `json:"kind"`
		Deferred bool   `json:"deferred,omitempty"`
	}
	var misses []miss
	check := func(labels []string, kind string) {
		for _, l := range labels {
			if !everything && !selected[l] {
				misses = append(misses, miss{Label: l, Kind: kind, Deferred: deferred[l]})
			}
		}
	}
	check(run.FailedTests, "test")
	check(run.FailedBuilds, "build")

	verdict := struct {
		FullRunTests   int    `json:"full_run_tests"`
		SelectedTests  int    `json:"selected_tests"`
		FailedTests    int    `json:"failed_tests"`
		FailedBuilds   int    `json:"failed_builds"`
		Misses         []miss `json:"misses"`
		SelectionIsAll bool   `json:"selection_is_everything"`
	}{run.Tests, 0, len(run.FailedTests), len(run.FailedBuilds), misses, everything}
	if tests, err := report.ReadLines(outPath(*selDir, report.TestTargetsFile)); err == nil {
		verdict.SelectedTests = len(tests)
	}
	if verdict.Misses == nil {
		verdict.Misses = []miss{}
	}
	if err := report.WriteJSON(outPath(*selDir, "verify.json"), verdict); err != nil {
		return err
	}
	logf("full run: %d tests, %d failed tests, %d failed builds; selection: %d tests; misses: %d",
		run.Tests, len(run.FailedTests), len(run.FailedBuilds), verdict.SelectedTests, len(misses))
	for _, m := range misses {
		note := ""
		if m.Deferred {
			note = " (deferred by --max-test-distance)"
		}
		logf("MISS: %s %s failed but was not selected%s", m.Kind, m.Label, note)
	}
	if len(misses) > 0 && *failOnMiss {
		return errMisses
	}
	return nil
}
