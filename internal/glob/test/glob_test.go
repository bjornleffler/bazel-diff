// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Tests for the glob package.

package test

import (
	"testing"

	"github.com/bjornleffler/bazel-diff2/internal/glob"
)

// TestMatch covers each wildcard form and root anchoring.
func TestMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*.md", "README.md", true},
		{"*.md", "docs/guide/intro.md", true},
		{"*.md", "docs/guide/intro.mdx", false},
		{"/MODULE.bazel", "MODULE.bazel", true},
		{"/MODULE.bazel", "third_party/x/MODULE.bazel", false},
		{"MODULE.bazel", "third_party/x/MODULE.bazel", true},
		{"docs/**", "docs/a/b.txt", true},
		{"docs/**", "src/docs/a.txt", false},
		{"**/testdata/*.json", "a/b/testdata/x.json", true},
		{"**/testdata/*.json", "testdata/x.json", true},
		{"**/testdata/*.json", "testdata/sub/x.json", false},
		{"src/*/BUILD", "src/a/BUILD", true},
		{"src/*/BUILD", "src/a/b/BUILD", false},
		{"file?.txt", "file1.txt", true},
		{"file?.txt", "file10.txt", false},
		{"a.b", "axb", false},
	}
	for _, c := range cases {
		p, err := glob.Compile(c.pattern)
		if err != nil {
			t.Fatalf("Compile(%q): %v", c.pattern, err)
		}
		if got := p.Match(c.path); got != c.want {
			t.Errorf("%q.Match(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// TestSet checks that a set matches when any member does.
func TestSet(t *testing.T) {
	set, err := glob.CompileAll([]string{"*.md", "docs/**"})
	if err != nil {
		t.Fatal(err)
	}
	if !set.Match("docs/x.txt") || !set.Match("a/README.md") || set.Match("src/main.go") {
		t.Error("set matched the wrong paths")
	}
	if _, err := glob.Compile(""); err == nil {
		t.Error("empty pattern compiled")
	}
}
