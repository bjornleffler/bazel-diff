// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Target patterns, the "//..." and "//pkg:all" arguments that `bazel build`
// takes, restricted to the main repository.

package label

import (
	"fmt"
	"strconv"
	"strings"
)

// Pattern is one target pattern in the main repository: "//...",
// "//pkg/...", "//pkg:all", "//pkg:*", "//pkg:name" or "//pkg". A leading
// "-" makes it a negative pattern that removes targets, as in Bazel.
type Pattern struct {
	// Negative patterns remove the targets they match.
	Negative bool
	// Package is the package the pattern names, or the root of a recursive one.
	Package string
	// Recursive patterns ("/...") match Package and every package below it.
	Recursive bool
	// AllInPackage patterns (":all", ":*", ":all-targets") match any name.
	AllInPackage bool
	// Name is the single target a non-wildcard pattern matches.
	Name string
	// text is the pattern as the user wrote it.
	text string
}

// Resolve turns a pattern relative to the package dir (the current directory
// relative to the workspace root; "" at the root) into an absolute one, as
// Bazel does: "..." becomes "//dir/...", ":all" becomes "//dir:all" and
// "sub:name" becomes "//dir/sub:name". A leading "-" is kept. Absolute
// patterns and patterns in other repositories are returned unchanged.
func Resolve(s, dir string) string {
	if !IsRelative(s) {
		return s
	}
	sign, rest := "", s
	if strings.HasPrefix(rest, "-") {
		sign, rest = "-", rest[1:]
	}
	switch {
	case dir == "":
		return sign + "//" + rest
	case strings.HasPrefix(rest, ":"):
		return sign + "//" + dir + rest
	default:
		return sign + "//" + dir + "/" + rest
	}
}

// IsRelative reports whether a pattern, with or without a leading "-",
// depends on the current directory: it starts neither with "//" nor "@".
func IsRelative(s string) bool {
	rest := strings.TrimPrefix(s, "-")
	return !strings.HasPrefix(rest, "//") && !strings.HasPrefix(rest, "@")
}

// ParsePattern parses one absolute target pattern; use Resolve first for
// relative ones. Relative patterns and patterns in external repositories are
// rejected.
func ParsePattern(s string) (Pattern, error) {
	p := Pattern{text: s}
	rest := s
	if strings.HasPrefix(rest, "-") {
		p.Negative = true
		rest = rest[1:]
	}
	rest = Normalize(rest)
	if !strings.HasPrefix(rest, "//") {
		return Pattern{}, fmt.Errorf("target pattern %q must start with // (main repository, absolute)", s)
	}
	rest = rest[2:]

	pkg, target, hasTarget := strings.Cut(rest, ":")
	switch {
	case pkg == "...":
		p.Recursive = true
		pkg = ""
	case strings.HasSuffix(pkg, "/..."):
		p.Recursive = true
		pkg = strings.TrimSuffix(pkg, "/...")
	}
	if strings.Contains(pkg, "...") {
		return Pattern{}, fmt.Errorf("target pattern %q: \"...\" may only end the package path", s)
	}
	p.Package = pkg

	isWildcard := target == "all" || target == "*" || target == "all-targets"
	switch {
	case p.Recursive:
		// "//pkg/..." is shorthand for "//pkg/...:all".
		if hasTarget && !isWildcard {
			return Pattern{}, fmt.Errorf("target pattern %q: a recursive pattern needs :all, :* or no target name", s)
		}
		p.AllInPackage = true
	case isWildcard:
		p.AllInPackage = true
	case hasTarget && target != "":
		p.Name = target
	case !hasTarget && pkg != "":
		// "//foo/bar" is shorthand for "//foo/bar:bar".
		p.Name = pkg[strings.LastIndex(pkg, "/")+1:]
	default:
		return Pattern{}, fmt.Errorf("target pattern %q names no target", s)
	}
	return p, nil
}

// Matches reports whether the pattern covers the label, ignoring Negative.
func (p Pattern) Matches(l Label) bool {
	if !l.IsMain() {
		return false
	}
	if p.Recursive {
		return p.Package == "" || l.Package == p.Package || strings.HasPrefix(l.Package, p.Package+"/")
	}
	return l.Package == p.Package && (p.AllInPackage || l.Name == p.Name)
}

// String returns the pattern as it was written.
func (p Pattern) String() string {
	return p.text
}

// Filter returns the labels that a sequence of patterns selects, keeping
// their order. As in Bazel, patterns apply left to right: a positive pattern
// adds the targets it matches and a negative one removes them, so a label is
// kept when the last pattern that matches it is positive. Labels that do not
// parse are dropped.
func Filter(labels []string, patterns []Pattern) []string {
	var kept []string
	for _, s := range labels {
		l, err := Parse(s)
		if err != nil {
			continue
		}
		selected := false
		for _, p := range patterns {
			if p.Matches(l) {
				selected = !p.Negative
			}
		}
		if selected {
			kept = append(kept, s)
		}
	}
	return kept
}

// DepsQuery returns a `bazel query` expression for the targets the patterns
// select and everything they depend on. Nothing else can change the hash of
// a selected target, so hashing this is enough to select among the
// patterns' targets, at a fraction of the cost of the whole repository, and
// broken packages elsewhere do not get in the way. Patterns combine left to
// right as in Bazel: "//a/... -//a/gen/..." becomes
// deps(("//a/..." - "//a/gen/...")).
func DepsQuery(patterns []Pattern) string {
	expr := "set()"
	for i, p := range patterns {
		quoted := strconv.Quote(strings.TrimPrefix(p.String(), "-"))
		switch {
		case i == 0 && !p.Negative:
			expr = quoted
		case p.Negative:
			expr = "(" + expr + " - " + quoted + ")"
		default:
			expr = "(" + expr + " + " + quoted + ")"
		}
	}
	return "deps(" + expr + ")"
}
