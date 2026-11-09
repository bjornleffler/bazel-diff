// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Tests for the label package.

package test

import (
	"testing"

	"github.com/bjornleffler/bazel-diff/internal/label"
)

// TestParse covers main, external and shorthand labels.
func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want label.Label
		path string
	}{
		{"//pkg:name", label.Label{Package: "pkg", Name: "name"}, "pkg/name"},
		{"@@//pkg/sub:file.txt", label.Label{Package: "pkg/sub", Name: "file.txt"}, "pkg/sub/file.txt"},
		{"@@rules_cc+//cc:defs.bzl", label.Label{Repo: "rules_cc+", Package: "cc", Name: "defs.bzl"}, "cc/defs.bzl"},
		{"//:root.txt", label.Label{Name: "root.txt"}, "root.txt"},
		{"//foo/bar", label.Label{Package: "foo/bar", Name: "bar"}, "foo/bar/bar"},
		{"//pkg:dir/nested.txt", label.Label{Package: "pkg", Name: "dir/nested.txt"}, "pkg/dir/nested.txt"},
	}
	for _, c := range cases {
		got, err := label.Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		if got != c.want || got.Path() != c.path {
			t.Errorf("Parse(%q) = %+v (path %q), want %+v (path %q)", c.in, got, got.Path(), c.want, c.path)
		}
	}
	for _, bad := range []string{"pkg:name", "@@repo", ":name"} {
		if _, err := label.Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}

// TestNormalize checks that only main-repository prefixes are removed.
func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"@@//a:b":             "//a:b",
		"@//a:b":              "//a:b",
		"//a:b":               "//a:b",
		"@@rules_go+//go:def": "@@rules_go+//go:def",
	} {
		if got := label.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if !label.IsExternal("@@x+//a:b") || label.IsExternal("//a:b") {
		t.Error("IsExternal is wrong")
	}
}
