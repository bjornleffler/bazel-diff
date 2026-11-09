// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Where a git repository sits within its Bazel workspace.

package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

// Markers are the files whose directory Bazel takes as the workspace root.
var Markers = []string{"MODULE.bazel", "REPO.bazel", "WORKSPACE", "WORKSPACE.bazel"}

// Layout places a git repository within its Bazel workspace. Usually the two
// roots are the same directory. The workspace can also sit above the
// repository: ~/git may hold MODULE.bazel and several repositories side by
// side, each one a package tree such as //bucketfs/....
type Layout struct {
	// Root is the Bazel workspace root.
	Root string
	// Prefix is the repository's path below Root in slash form, or "" when
	// the repository root is the workspace root.
	Prefix string
}

// Checkout is one tree to hash: a git working tree at GitDir, inside the
// Bazel workspace that Layout describes. GitDir is Root joined with Prefix.
type Checkout struct {
	GitDir string
	Layout
}

// IsWorkspaceRoot reports whether dir holds one of the Markers as a file.
// Directories do not count: on a case-insensitive file system a directory
// called "workspace" would otherwise match "WORKSPACE".
func IsWorkspaceRoot(dir string) bool {
	for _, marker := range Markers {
		if fi, err := os.Stat(filepath.Join(dir, marker)); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// FindLayout finds the Bazel workspace around the git repository at gitTop:
// the nearest of gitTop and its parents that IsWorkspaceRoot. When there is
// none, gitTop is taken as the root and Bazel reports the problem itself.
func FindLayout(gitTop string) Layout {
	for dir := gitTop; ; dir = filepath.Dir(dir) {
		if IsWorkspaceRoot(dir) {
			rel, err := filepath.Rel(dir, gitTop)
			if err != nil || rel == "." {
				return Layout{Root: gitTop}
			}
			return Layout{Root: dir, Prefix: filepath.ToSlash(rel)}
		}
		if filepath.Dir(dir) == dir {
			return Layout{Root: gitTop}
		}
	}
}

// WorkspacePath turns a repository-relative path into a workspace-relative
// one, the form that labels and seed globs use.
func (l Layout) WorkspacePath(repoPath string) string {
	if l.Prefix == "" {
		return repoPath
	}
	return l.Prefix + "/" + repoPath
}

// RepoPath turns a workspace-relative path into a repository-relative one,
// reporting false when the path lies outside the repository.
func (l Layout) RepoPath(workspacePath string) (string, bool) {
	if l.Prefix == "" {
		return workspacePath, true
	}
	rest, ok := strings.CutPrefix(workspacePath, l.Prefix+"/")
	return rest, ok
}

// mirror makes dst stand in for the workspace at src, around a checkout of
// the repository that the caller places at dst/prefix. Along the path to the
// repository, every other entry of each directory in src gets a symlink in
// dst. Symlinks in src are copied as they are, so a relative one such as
// MODULE.bazel -> bucketfs/MODULE.bazel.bucketfs resolves into the checkout
// and picks up its revision; anything else points at the original, so other
// repositories and root files such as .bazelrc are the live ones. Bazel's
// convenience symlinks (bazel-* symlinks) at the root are left out, and a plain
// MODULE.bazel.lock is copied rather than linked, because Bazel rewrites it
// and must not rewrite the real one with the base revision's resolution.
// Each call rebuilds the mirror, so entries added to or removed from src
// since the last run are picked up.
func mirror(src, dst, prefix string) error {
	parts := strings.Split(prefix, "/")
	for i, next := range parts {
		sub := filepath.FromSlash(strings.Join(parts[:i], "/"))
		if err := mirrorDir(filepath.Join(src, sub), filepath.Join(dst, sub), next, i == 0); err != nil {
			return err
		}
	}
	return nil
}

// mirrorDir fills the directory to with entries for those of from, except
// skip (the next directory on the way to the checkout) and, at the workspace
// root, Bazel's convenience symlinks.
func mirrorDir(from, to, skip string, root bool) error {
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	old, err := os.ReadDir(to)
	if err != nil {
		return err
	}
	for _, e := range old {
		// Everything here is ours or Bazel's (its lock file and convenience
		// symlinks), except the directory on the way to the checkout.
		if e.Name() == skip {
			continue
		}
		if err := os.RemoveAll(filepath.Join(to, e.Name())); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		isLink := e.Type()&os.ModeSymlink != 0
		// Convenience symlinks are symlinks; a real directory such as a local
		// bazel-registry is part of the workspace and must be mirrored.
		if name == skip || (root && isLink && strings.HasPrefix(name, "bazel-")) {
			continue
		}
		target := filepath.Join(from, name)
		if root && name == lockFile && e.Type().IsRegular() {
			if err := copyFile(target, filepath.Join(to, name)); err != nil {
				return err
			}
			continue
		}
		if isLink {
			if target, err = os.Readlink(target); err != nil {
				return err
			}
		}
		if err := os.Symlink(target, filepath.Join(to, name)); err != nil {
			return err
		}
	}
	return nil
}

// lockFile is the file Bazel writes in the workspace root.
const lockFile = "MODULE.bazel.lock"

// copyFile copies a regular file's content to dst.
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
