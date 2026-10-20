// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package label parses the canonical labels that `bazel query` prints when it
// runs with --consistent_labels.
package label

import (
	"fmt"
	"strings"
)

// Label is a parsed Bazel label. Repo is empty for the main repository.
type Label struct {
	Repo    string
	Package string
	Name    string
}

// Parse splits a label such as "@@rules_cc+//cc:defs.bzl", "@@//pkg:name" or
// "//pkg:name" into its repository, package and target name.
func Parse(s string) (Label, error) {
	var l Label
	rest := s
	if strings.HasPrefix(rest, "@") {
		rest = strings.TrimLeft(rest, "@")
		i := strings.Index(rest, "//")
		if i < 0 {
			return Label{}, fmt.Errorf("label %q has no package part", s)
		}
		l.Repo, rest = rest[:i], rest[i:]
	}
	if !strings.HasPrefix(rest, "//") {
		return Label{}, fmt.Errorf("label %q is not absolute", s)
	}
	rest = rest[2:]
	if i := strings.Index(rest, ":"); i >= 0 {
		l.Package, l.Name = rest[:i], rest[i+1:]
	} else {
		// "//foo/bar" is shorthand for "//foo/bar:bar".
		l.Package = rest
		l.Name = rest[strings.LastIndex(rest, "/")+1:]
	}
	return l, nil
}

// Normalize rewrites main-repository labels ("@@//pkg:name" or "@//pkg:name")
// to the "//pkg:name" form that users type and leaves every other label as is.
func Normalize(s string) string {
	if strings.HasPrefix(s, "@@//") {
		return s[2:]
	}
	if strings.HasPrefix(s, "@//") {
		return s[1:]
	}
	return s
}

// IsExternal reports whether a normalized label belongs to an external
// repository.
func IsExternal(s string) bool {
	return strings.HasPrefix(s, "@")
}

// IsMain reports whether the label belongs to the main repository.
func (l Label) IsMain() bool {
	return l.Repo == ""
}

// Path returns the repository-relative path of a file target, such as
// "pkg/sub/file.txt" for "//pkg:sub/file.txt".
func (l Label) Path() string {
	if l.Package == "" {
		return l.Name
	}
	return l.Package + "/" + l.Name
}

// String formats the label in normalized form.
func (l Label) String() string {
	if l.Repo == "" {
		return "//" + l.Package + ":" + l.Name
	}
	return "@@" + l.Repo + "//" + l.Package + ":" + l.Name
}
