// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Package fastpath decides, from the list of changed files alone, that a
// change cannot affect any target, so the head revision need not be queried.
//
// The check is opt-in and deliberately narrow. A file content change can only
// matter to Bazel if the file is a source target, is evaluated by Bazel
// (BUILD, .bzl, MODULE.bazel), is a seed, or is read by a repository rule or
// module extension. The last is invisible from here: go.mod, for example, is
// read by Gazelle's go_deps extension without being a target. So the caller
// names the files that are safe to skip (documentation, say), and this
// package additionally refuses anything that could still matter.
package fastpath

import (
	"fmt"
	"path"
	"strings"

	"github.com/bjornleffler/bazel-diff/internal/git"
	"github.com/bjornleffler/bazel-diff/internal/glob"
	"github.com/bjornleffler/bazel-diff/internal/label"
	"github.com/bjornleffler/bazel-diff/internal/snapshot"
)

// bazelFiles are evaluated by Bazel itself, so they are never skipped.
var bazelFiles = map[string]bool{
	"BUILD":            true,
	"BUILD.bazel":      true,
	"REPO.bazel":       true,
	"MODULE.bazel":     true,
	"WORKSPACE":        true,
	"WORKSPACE.bazel":  true,
	"WORKSPACE.bzlmod": true,
	".bazelignore":     true,
}

// Check reports whether every change is skippable, and if not, the first
// change that is not.
//
// A change is skippable when it modifies (not adds, deletes or renames) a
// file that matches allow, is not a Bazel file or seed, and is neither a
// source target in base nor inside a source directory target. Additions and
// deletions are never skipped because a glob() may pick them up.
func Check(changes []git.Change, base *snapshot.Snapshot, allow, seeds glob.Set) (bool, string) {
	if len(allow) == 0 {
		return false, "no fast-path patterns"
	}
	sources := make(map[string]bool)
	for i := range base.Targets {
		t := &base.Targets[i]
		if t.Kind != snapshot.KindSourceFile || t.IsExternal() {
			continue
		}
		if l, err := label.Parse(t.Label); err == nil {
			sources[l.Path()] = true
		}
	}
	for _, c := range changes {
		name := path.Base(c.Path)
		switch {
		case c.Status != 'M':
			return false, fmt.Sprintf("%s was added, deleted or changed type", c.Path)
		case !allow.Match(c.Path):
			return false, fmt.Sprintf("%s does not match a fast-path pattern", c.Path)
		case bazelFiles[name] || strings.HasSuffix(name, ".bzl") || strings.HasSuffix(name, ".MODULE.bazel"):
			return false, fmt.Sprintf("%s is read by Bazel", c.Path)
		case seeds.Match(c.Path):
			return false, fmt.Sprintf("%s is a seed file", c.Path)
		}
		for p := c.Path; p != "."; p = path.Dir(p) {
			if sources[p] {
				return false, fmt.Sprintf("%s is used by a target", c.Path)
			}
			if !strings.Contains(p, "/") {
				break
			}
		}
	}
	return true, fmt.Sprintf("all %d changed files match fast-path patterns", len(changes))
}
