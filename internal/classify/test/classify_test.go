// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Tests for the classify package.

package test

import (
	"reflect"
	"testing"

	"github.com/bjornleffler/bazel-diff2/internal/classify"
	"github.com/bjornleffler/bazel-diff2/internal/diff"
	"github.com/bjornleffler/bazel-diff2/internal/snapshot"
)

// result is a diff result with one target of each interesting sort.
func result() *diff.Result {
	return &diff.Result{Impacted: []diff.Impacted{
		{Label: "//a:lib", Kind: "go_library", Distance: 0},
		{Label: "//a:lib_test", Kind: "go_test", Test: true, Distance: 1},
		{Label: "//a:far_test", Kind: "go_test", Test: true, Distance: 5},
		{Label: "//a:manual_test", Kind: "go_test", Test: true, Tags: []string{"manual"}, Distance: 1},
		{Label: "//a:slow_test", Kind: "go_test", Test: true, Tags: []string{"slow"}, Distance: 1},
		{Label: "//a:image", Kind: "oci_image", Tags: []string{"docker"}, Distance: 1},
		{Label: "//a:suite", Kind: "test_suite", Distance: 1},
		{Label: "//a:lib.go", Kind: snapshot.KindSourceFile},
		{Label: "@@dep+//:x", Kind: "filegroup", Distance: 1},
	}}
}

// TestDefaults drops manual, suites, files and external targets.
func TestDefaults(t *testing.T) {
	s := classify.Select(result(), classify.Options{MaxTestDistance: -1})
	if want := []string{"//a:lib", "//a:image"}; !reflect.DeepEqual(s.Build, want) {
		t.Errorf("build = %v, want %v", s.Build, want)
	}
	if want := []string{"//a:lib_test", "//a:far_test", "//a:slow_test"}; !reflect.DeepEqual(s.Tests, want) {
		t.Errorf("tests = %v, want %v", s.Tests, want)
	}
	if s.SkippedManual != 1 || s.SkippedExternal != 1 || s.SkippedTestSuite != 1 {
		t.Errorf("skipped counts = %+v", s)
	}
}

// TestFilters applies tag filters, manual inclusion and test distance.
func TestFilters(t *testing.T) {
	s := classify.Select(result(), classify.Options{
		IncludeManual:   true,
		BuildTagFilters: []string{"-docker"},
		TestTagFilters:  []string{"-slow"},
		MaxTestDistance: 2,
	})
	if want := []string{"//a:lib"}; !reflect.DeepEqual(s.Build, want) {
		t.Errorf("build = %v, want %v", s.Build, want)
	}
	if want := []string{"//a:lib_test", "//a:manual_test"}; !reflect.DeepEqual(s.Tests, want) {
		t.Errorf("tests = %v, want %v", s.Tests, want)
	}
	if want := []string{"//a:far_test"}; !reflect.DeepEqual(s.DeferredTests, want) {
		t.Errorf("deferred = %v, want %v", s.DeferredTests, want)
	}
	if s.SkippedTagFilter != 2 {
		t.Errorf("tag filter skips = %d, want 2", s.SkippedTagFilter)
	}
}

// TestFullBuild selects everything.
func TestFullBuild(t *testing.T) {
	r := result()
	r.FullBuild = true
	s := classify.Select(r, classify.Options{MaxTestDistance: -1})
	if !s.FullBuild || !reflect.DeepEqual(s.Build, []string{"//..."}) || !reflect.DeepEqual(s.Tests, []string{"//..."}) {
		t.Errorf("selection = %+v", s)
	}
}

// TestMatchesTagFilters follows Bazel's semantics.
func TestMatchesTagFilters(t *testing.T) {
	cases := []struct {
		tags, filters []string
		want          bool
	}{
		{nil, nil, true},
		{[]string{"a"}, []string{"-a"}, false},
		{[]string{"a"}, []string{"b"}, false},
		{[]string{"a", "b"}, []string{"b", "-c"}, true},
		{[]string{"a", "c"}, []string{"a", "-c"}, false},
		{[]string{"a"}, []string{"+a"}, true},
	}
	for _, c := range cases {
		if got := classify.MatchesTagFilters(c.tags, c.filters); got != c.want {
			t.Errorf("MatchesTagFilters(%v, %v) = %v", c.tags, c.filters, got)
		}
	}
	if got := classify.ParseTagFilters(" a, -b ,,"); !reflect.DeepEqual(got, []string{"a", "-b"}) {
		t.Errorf("ParseTagFilters = %v", got)
	}
}
