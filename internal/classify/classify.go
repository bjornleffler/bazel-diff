// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package classify turns a list of impacted targets into what CI should
// actually build and test.
package classify

import (
	"strings"

	"github.com/bjornleffler/bazel-diff2/internal/diff"
	"github.com/bjornleffler/bazel-diff2/internal/snapshot"
)

// Everything is the target pattern written when the whole repository must be
// built.
const Everything = "//..."

// Options controls the selection.
type Options struct {
	// IncludeManual keeps targets tagged "manual". Bazel skips them in "//..."
	// but builds them when named explicitly, which a target list does, so by
	// default they are dropped here.
	IncludeManual bool
	// BuildTagFilters and TestTagFilters use Bazel's --build_tag_filters and
	// --test_tag_filters syntax, one entry per comma-separated item. Pass the
	// same values the pipeline passes to Bazel.
	BuildTagFilters []string
	TestTagFilters  []string
	// MaxTestDistance defers tests further than this many dependency edges
	// from a direct change. Negative means no limit.
	MaxTestDistance int
}

// Selection is what CI should build and test.
type Selection struct {
	FullBuild bool
	// Build holds non-test rules; tests are built by `bazel test`.
	Build []string
	Tests []string
	// DeferredTests were impacted but are beyond MaxTestDistance.
	DeferredTests []string

	SkippedManual    int
	SkippedTagFilter int
	SkippedExternal  int
	SkippedTestSuite int
}

// Select applies Options to a diff result.
func Select(r *diff.Result, opts Options) Selection {
	if r.FullBuild {
		return Selection{FullBuild: true, Build: []string{Everything}, Tests: []string{Everything}}
	}
	var s Selection
	for i := range r.Impacted {
		t := &r.Impacted[i]
		switch {
		case t.External():
			s.SkippedExternal++
			continue
		case isFileOrGroup(t.Kind):
			continue
		case t.Kind == "test_suite":
			// A suite runs all of its tests, impacted or not; the impacted ones
			// are already listed individually.
			s.SkippedTestSuite++
			continue
		case !opts.IncludeManual && hasTag(t.Tags, "manual"):
			s.SkippedManual++
			continue
		case !MatchesTagFilters(t.Tags, opts.BuildTagFilters):
			s.SkippedTagFilter++
			continue
		}
		if !t.Test {
			s.Build = append(s.Build, t.Label)
			continue
		}
		if !MatchesTagFilters(t.Tags, opts.TestTagFilters) {
			s.SkippedTagFilter++
			continue
		}
		if opts.MaxTestDistance >= 0 && (t.Distance < 0 || t.Distance > opts.MaxTestDistance) {
			s.DeferredTests = append(s.DeferredTests, t.Label)
			continue
		}
		s.Tests = append(s.Tests, t.Label)
	}
	return s
}

// ParseTagFilters splits a comma-separated Bazel tag filter flag value.
func ParseTagFilters(value string) []string {
	var filters []string
	for _, f := range strings.Split(value, ",") {
		if f = strings.TrimSpace(f); f != "" {
			filters = append(filters, f)
		}
	}
	return filters
}

// MatchesTagFilters applies Bazel's tag filter rules: a target with any
// excluded ("-tag") tag fails; otherwise, if there are required tags, the
// target needs at least one of them.
func MatchesTagFilters(tags, filters []string) bool {
	var required []string
	for _, f := range filters {
		if excluded, ok := strings.CutPrefix(f, "-"); ok {
			if hasTag(tags, excluded) {
				return false
			}
			continue
		}
		required = append(required, strings.TrimPrefix(f, "+"))
	}
	if len(required) == 0 {
		return true
	}
	for _, r := range required {
		if hasTag(tags, r) {
			return true
		}
	}
	return false
}

// hasTag reports whether tags contains tag.
func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// isFileOrGroup reports whether a kind is a file or group rather than a rule.
func isFileOrGroup(kind string) bool {
	switch kind {
	case snapshot.KindSourceFile, snapshot.KindGeneratedFile, snapshot.KindPackageGroup, snapshot.KindEnvironmentGroup:
		return true
	}
	return false
}
