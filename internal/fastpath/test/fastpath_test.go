// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Tests for the fastpath package.

package test

import (
	"testing"

	"github.com/bjornleffler/bazel-diff2/internal/fastpath"
	"github.com/bjornleffler/bazel-diff2/internal/git"
	"github.com/bjornleffler/bazel-diff2/internal/glob"
	"github.com/bjornleffler/bazel-diff2/internal/snapshot"
)

// TestCheck walks through each reason a change may not be skipped.
func TestCheck(t *testing.T) {
	base := &snapshot.Snapshot{Targets: []snapshot.Target{
		{Label: "//docs:used.md", Kind: snapshot.KindSourceFile},
		{Label: "//pkg:testdata", Kind: snapshot.KindSourceFile},
		{Label: "@@ext+//:README.md", Kind: snapshot.KindSourceFile},
	}}
	allow, _ := glob.CompileAll([]string{"*.md", "**/*.txt", "*.bzl", "*.bazelrc"})
	seeds, _ := glob.CompileAll([]string{"*.bazelrc"})
	cases := []struct {
		change git.Change
		want   bool
	}{
		{git.Change{Status: 'M', Path: "README.md"}, true},
		{git.Change{Status: 'M', Path: "docs/other.md"}, true},
		{git.Change{Status: 'A', Path: "docs/new.md"}, false},
		{git.Change{Status: 'D', Path: "docs/old.md"}, false},
		{git.Change{Status: 'M', Path: "src/main.go"}, false},
		{git.Change{Status: 'M', Path: "docs/used.md"}, false},
		{git.Change{Status: 'M', Path: "pkg/testdata/x.txt"}, false},
		{git.Change{Status: 'M', Path: "tools/defs.bzl"}, false},
		{git.Change{Status: 'M', Path: "ci.bazelrc"}, false},
	}
	for _, c := range cases {
		got, reason := fastpath.Check([]git.Change{c.change}, base, allow, seeds)
		if got != c.want {
			t.Errorf("%c %s: skip=%v (%s), want %v", c.change.Status, c.change.Path, got, reason, c.want)
		}
	}
	if ok, _ := fastpath.Check(nil, base, nil, seeds); ok {
		t.Error("fast path applied without patterns")
	}
}
