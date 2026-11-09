// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Tests for target patterns.

package test

import (
	"reflect"
	"testing"

	"github.com/bjornleffler/bazel-diff/internal/label"
)

// mustPatterns parses patterns or fails the test.
func mustPatterns(t *testing.T, texts ...string) []label.Pattern {
	t.Helper()
	var ps []label.Pattern
	for _, s := range texts {
		p, err := label.ParsePattern(s)
		if err != nil {
			t.Fatalf("ParsePattern(%q): %v", s, err)
		}
		ps = append(ps, p)
	}
	return ps
}

// TestPatternMatches checks which labels each kind of pattern covers.
func TestPatternMatches(t *testing.T) {
	cases := []struct {
		pattern string
		yes     []string
		no      []string
	}{
		{"//...", []string{"//:root", "//a:x", "//a/b:y"}, []string{"@@ext+//a:x"}},
		{"//a/...", []string{"//a:x", "//a/b:y"}, []string{"//ab:x", "//:a", "//b/a:x"}},
		{"//a/...:all", []string{"//a/b:y"}, []string{"//b:y"}},
		{"//a:all", []string{"//a:x", "//a:y"}, []string{"//a/b:x"}},
		{"//a:*", []string{"//a:x"}, []string{"//a/b:x"}},
		{"//a:x", []string{"//a:x"}, []string{"//a:y", "//a/b:x"}},
		{"//a/b", []string{"//a/b:b"}, []string{"//a/b:c"}},
		{"@@//a:x", []string{"//a:x"}, nil},
		{"//:all", []string{"//:root"}, []string{"//a:x"}},
	}
	for _, c := range cases {
		p := mustPatterns(t, c.pattern)[0]
		for _, s := range c.yes {
			l, _ := label.Parse(s)
			if !p.Matches(l) {
				t.Errorf("%q should match %q", c.pattern, s)
			}
		}
		for _, s := range c.no {
			l, _ := label.Parse(s)
			if p.Matches(l) {
				t.Errorf("%q should not match %q", c.pattern, s)
			}
		}
	}
}

// TestParsePatternErrors rejects relative, external and malformed patterns.
func TestParsePatternErrors(t *testing.T) {
	for _, bad := range []string{"...", "a/...", "@@ext+//...", "//a/.../b", "//a/...:x", "//", "//a:"} {
		if _, err := label.ParsePattern(bad); err == nil {
			t.Errorf("ParsePattern(%q) succeeded", bad)
		}
	}
}

// TestFilter applies positive and negative patterns in order.
func TestFilter(t *testing.T) {
	labels := []string{"//a:x", "//a/gen:y", "//b:z", "@@ext+//a:x"}
	cases := []struct {
		patterns []string
		want     []string
	}{
		{[]string{"//..."}, []string{"//a:x", "//a/gen:y", "//b:z"}},
		{[]string{"//a/..."}, []string{"//a:x", "//a/gen:y"}},
		{[]string{"//...", "-//a/gen/..."}, []string{"//a:x", "//b:z"}},
		{[]string{"-//a/...", "//a:x"}, []string{"//a:x"}},
		{[]string{"//b:z", "//a:x"}, []string{"//a:x", "//b:z"}},
		{[]string{"//c/..."}, nil},
	}
	for _, c := range cases {
		got := label.Filter(labels, mustPatterns(t, c.patterns...))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Filter(%q) = %q, want %q", c.patterns, got, c.want)
		}
	}
}
