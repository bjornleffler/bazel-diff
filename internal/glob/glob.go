// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package glob matches slash-separated, repository-relative paths against
// shell-style patterns that also understand "**".
package glob

import (
	"fmt"
	"regexp"
	"strings"
)

// Pattern is one compiled glob.
//
//   - "*" matches any run of characters except "/".
//   - "?" matches one character except "/".
//   - "**" matches any number of whole path segments, including none.
//   - A pattern with no "/" matches the base name at any depth, so "*.md"
//     matches both "README.md" and "docs/guide/intro.md".
//   - A leading "/" anchors the pattern at the repository root, so
//     "/MODULE.bazel" matches only the root module file.
type Pattern struct {
	raw string
	re  *regexp.Regexp
}

// Compile turns a glob into a Pattern.
func Compile(pattern string) (*Pattern, error) {
	if pattern == "" {
		return nil, fmt.Errorf("empty glob pattern")
	}
	anchored := strings.HasPrefix(pattern, "/")
	p := strings.TrimPrefix(pattern, "/")
	var b strings.Builder
	b.WriteString("^")
	if !anchored && !strings.Contains(p, "/") {
		b.WriteString("(?:.*/)?")
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			// "**/" swallows leading segments, a trailing "/**" (or a bare
			// "**") swallows everything after it.
			i++
			if i+1 < len(p) && p[i+1] == '/' {
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("glob %q: %w", pattern, err)
	}
	return &Pattern{raw: pattern, re: re}, nil
}

// Match reports whether path matches the pattern.
func (p *Pattern) Match(path string) bool {
	return p.re.MatchString(path)
}

// String returns the pattern as it was written.
func (p *Pattern) String() string {
	return p.raw
}

// Set is a list of patterns that matches a path when any member does.
type Set []*Pattern

// CompileAll compiles every pattern into a Set.
func CompileAll(patterns []string) (Set, error) {
	set := make(Set, 0, len(patterns))
	for _, raw := range patterns {
		p, err := Compile(raw)
		if err != nil {
			return nil, err
		}
		set = append(set, p)
	}
	return set, nil
}

// Match reports whether any pattern in the set matches path.
func (s Set) Match(path string) bool {
	for _, p := range s {
		if p.Match(path) {
			return true
		}
	}
	return false
}
