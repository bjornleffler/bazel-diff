// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Target patterns, the "//..." and "//pkg:all" arguments that `bazel build`
// takes, restricted to the main repository.

package label

import (
	"fmt"
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

// ParsePattern parses one target pattern. Relative patterns and patterns in
// external repositories are rejected.
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
