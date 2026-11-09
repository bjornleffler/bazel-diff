// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Package report writes a selection to the files a CI pipeline consumes.
//
// An output directory always holds:
//
//	build_targets.txt          one label per line, for --target_pattern_file
//	test_targets.txt           same, for `bazel test`
//	deferred_test_targets.txt  tests beyond --max-test-distance
//	impacted.json              every impacted target, with distance and cause
//	summary.json               counts, commits and full-build reasons
//	outputs.env                KEY=value lines for pipeline output variables
//
// On a full build the two target files contain just "//...".
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bjornleffler/bazel-diff/internal/classify"
	"github.com/bjornleffler/bazel-diff/internal/diff"
)

// File names inside an output directory.
const (
	BuildTargetsFile  = "build_targets.txt"
	TestTargetsFile   = "test_targets.txt"
	DeferredTestsFile = "deferred_test_targets.txt"
	ImpactedFile      = "impacted.json"
	SummaryFile       = "summary.json"
	OutputsEnvFile    = "outputs.env"
	BaseSnapshotFile  = "base.json.gz"
	HeadSnapshotFile  = "head.json.gz"
	AttributionFile   = "attribution.json"
)

// Summary is the content of summary.json.
type Summary struct {
	BaseCommit    string   `json:"base_commit"`
	HeadCommit    string   `json:"head_commit"`
	FullBuild     bool     `json:"full_build"`
	Reasons       []string `json:"reasons,omitempty"`
	FastPath      bool     `json:"fast_path,omitempty"`
	HeadTargets   int      `json:"head_targets"`
	Impacted      int      `json:"impacted"`
	Direct        int      `json:"direct"`
	Deleted       int      `json:"deleted"`
	Build         int      `json:"build"`
	Tests         int      `json:"tests"`
	DeferredTests int      `json:"deferred_tests"`
	Skipped       Skipped  `json:"skipped"`
	DurationMs    int64    `json:"duration_ms"`
}

// Skipped counts impacted targets left out of the selection, by reason.
type Skipped struct {
	Manual    int `json:"manual"`
	TagFilter int `json:"tag_filter"`
	External  int `json:"external"`
	TestSuite int `json:"test_suite"`
}

// Options carries the details that are not in the diff result itself.
type Options struct {
	FastPath   bool
	DurationMs int64
}

// Write writes every output file for one selection into dir.
func Write(dir string, r *diff.Result, s classify.Selection, opts Options) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	direct := 0
	for _, t := range r.Impacted {
		if t.Direct {
			direct++
		}
	}
	summary := Summary{
		BaseCommit:    r.BaseCommit,
		HeadCommit:    r.HeadCommit,
		FullBuild:     s.FullBuild,
		Reasons:       r.Reasons,
		FastPath:      opts.FastPath,
		HeadTargets:   r.HeadTargets,
		Impacted:      len(r.Impacted),
		Direct:        direct,
		Deleted:       len(r.Deleted),
		Build:         len(s.Build),
		Tests:         len(s.Tests),
		DeferredTests: len(s.DeferredTests),
		Skipped: Skipped{
			Manual:    s.SkippedManual,
			TagFilter: s.SkippedTagFilter,
			External:  s.SkippedExternal,
			TestSuite: s.SkippedTestSuite,
		},
		DurationMs: opts.DurationMs,
	}
	impacted := r.Impacted
	if impacted == nil {
		impacted = []diff.Impacted{}
	}

	writes := []struct {
		name string
		fn   func(string) error
	}{
		{BuildTargetsFile, func(p string) error { return writeLines(p, s.Build) }},
		{TestTargetsFile, func(p string) error { return writeLines(p, s.Tests) }},
		{DeferredTestsFile, func(p string) error { return writeLines(p, s.DeferredTests) }},
		{ImpactedFile, func(p string) error { return WriteJSON(p, impacted) }},
		{SummaryFile, func(p string) error { return WriteJSON(p, summary) }},
		{OutputsEnvFile, func(p string) error { return writeEnv(p, summary) }},
	}
	for _, w := range writes {
		if err := w.fn(filepath.Join(dir, w.name)); err != nil {
			return fmt.Errorf("writing %s: %w", w.name, err)
		}
	}
	return nil
}

// writeLines writes one entry per line.
func writeLines(path string, lines []string) error {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// WriteJSON writes v as indented JSON.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// writeEnv writes the values a pipeline needs as shell-sourceable lines.
func writeEnv(path string, s Summary) error {
	lines := []string{
		fmt.Sprintf("FULL_BUILD=%t", s.FullBuild),
		fmt.Sprintf("BUILD_COUNT=%d", s.Build),
		fmt.Sprintf("TEST_COUNT=%d", s.Tests),
		fmt.Sprintf("DEFERRED_TEST_COUNT=%d", s.DeferredTests),
		fmt.Sprintf("IMPACTED_COUNT=%d", s.Impacted),
		fmt.Sprintf("BASE_COMMIT=%s", s.BaseCommit),
		fmt.Sprintf("HEAD_COMMIT=%s", s.HeadCommit),
	}
	return writeLines(path, lines)
}

// ReadLines reads a target file written by Write, skipping blank lines and
// "#" comments.
func ReadLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	return lines, nil
}
