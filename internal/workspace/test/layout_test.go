// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Tests for repositories that sit below their Bazel workspace.

package test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bjornleffler/bazel-diff/internal/git"
	"github.com/bjornleffler/bazel-diff/internal/workspace"
)

// run runs git in dir and fails the test on error.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// write creates a file with its parent directories.
func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// read returns a file's content, following symlinks, or "" if it is missing.
func read(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// TestFindLayout finds the workspace at the repository root or above it,
// and ignores directories named like a marker.
func TestFindLayout(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "same", "MODULE.bazel"), "")
	write(t, filepath.Join(root, "ws", "MODULE.bazel"), "")
	os.MkdirAll(filepath.Join(root, "ws", "group", "repo", "workspace"), 0o755)
	os.MkdirAll(filepath.Join(root, "none"), 0o755)

	cases := []struct {
		gitTop string
		want   workspace.Layout
	}{
		{filepath.Join(root, "same"), workspace.Layout{Root: filepath.Join(root, "same")}},
		{filepath.Join(root, "ws", "group", "repo"), workspace.Layout{Root: filepath.Join(root, "ws"), Prefix: "group/repo"}},
		{filepath.Join(root, "none"), workspace.Layout{Root: filepath.Join(root, "none")}},
	}
	for _, c := range cases {
		if got := workspace.FindLayout(c.gitTop); got != c.want {
			t.Errorf("FindLayout(%s) = %+v, want %+v", c.gitTop, got, c.want)
		}
	}
}

// TestPaths converts between repository and workspace paths.
func TestPaths(t *testing.T) {
	l := workspace.Layout{Root: "/ws", Prefix: "repo"}
	if got := l.WorkspacePath("web/a.go"); got != "repo/web/a.go" {
		t.Errorf("WorkspacePath = %q", got)
	}
	if got, ok := l.RepoPath("repo/web/a.go"); !ok || got != "web/a.go" {
		t.Errorf("RepoPath(repo/web/a.go) = %q, %t", got, ok)
	}
	for _, outside := range []string{"other/a.go", "repository/a.go", ".bazelrc"} {
		if _, ok := l.RepoPath(outside); ok {
			t.Errorf("RepoPath(%q) should be outside the repository", outside)
		}
	}
	same := workspace.Layout{Root: "/ws"}
	if got, ok := same.RepoPath("web/a.go"); !ok || got != "web/a.go" || same.WorkspacePath("x") != "x" {
		t.Error("an empty prefix should leave paths alone")
	}
}

// TestMirroredCheckout checks out an older commit of a repository that sits
// below its workspace. The checkout must sit inside a mirror of the
// workspace in which the root MODULE.bazel symlink resolves into the
// checkout (the old revision), other entries are the live ones, and Bazel's
// convenience symlinks are left out. A second checkout picks up entries
// added to the workspace since the first.
func TestMirroredCheckout(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	repoDir := filepath.Join(ws, "repo")
	write(t, filepath.Join(repoDir, "MODULE.bazel.repo"), "module v1\n")
	write(t, filepath.Join(repoDir, "web", "BUILD"), "")
	run(t, repoDir, "init", "-q")
	run(t, repoDir, "add", "-A")
	run(t, repoDir, "commit", "-qm", "v1")
	old := gitHead(t, repoDir)
	write(t, filepath.Join(repoDir, "MODULE.bazel.repo"), "module v2\n")
	run(t, repoDir, "commit", "-qam", "v2")

	os.Symlink("repo/MODULE.bazel.repo", filepath.Join(ws, "MODULE.bazel"))
	write(t, filepath.Join(ws, ".bazelrc"), "build --x\n")
	write(t, filepath.Join(ws, "other", "BUILD"), "")
	os.Symlink(t.TempDir(), filepath.Join(ws, "bazel-bin"))
	write(t, filepath.Join(ws, "bazel-registry", "modules", "m", "metadata.json"), "{}")
	write(t, filepath.Join(ws, "MODULE.bazel.lock"), "live lock\n")

	layout := workspace.FindLayout(repoDir)
	if layout.Prefix != "repo" {
		t.Fatalf("FindLayout = %+v", layout)
	}
	w := workspace.Worktrees{Repo: git.Repo{Dir: repoDir}, Root: t.TempDir(), Layout: layout}
	co, err := w.Checkout(ctx, "base", old)
	if err != nil {
		t.Fatal(err)
	}
	if co.Prefix != "repo" || co.GitDir != filepath.Join(co.Root, "repo") {
		t.Fatalf("checkout = %+v", co)
	}
	if got := read(filepath.Join(co.Root, "MODULE.bazel")); got != "module v1\n" {
		t.Errorf("MODULE.bazel in the mirror = %q, want the old revision", got)
	}
	if got := read(filepath.Join(co.Root, ".bazelrc")); got != "build --x\n" {
		t.Errorf(".bazelrc in the mirror = %q", got)
	}
	if _, err := os.Stat(filepath.Join(co.Root, "other", "BUILD")); err != nil {
		t.Errorf("other repository missing from the mirror: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(co.Root, "bazel-bin")); err == nil {
		t.Error("convenience symlink bazel-bin should not be mirrored")
	}
	if _, err := os.Stat(filepath.Join(co.Root, "bazel-registry", "modules", "m", "metadata.json")); err != nil {
		t.Errorf("real directory bazel-registry should be mirrored: %v", err)
	}
	if !workspace.IsWorkspaceRoot(co.Root) {
		t.Error("the mirror is not a Bazel workspace")
	}
	// The lock file is a copy: Bazel rewriting it in the mirror must not
	// touch the real one, and a rewrite must not block the next refresh.
	mirrorLock := filepath.Join(co.Root, "MODULE.bazel.lock")
	if fi, err := os.Lstat(mirrorLock); err != nil || !fi.Mode().IsRegular() || read(mirrorLock) != "live lock\n" {
		t.Errorf("MODULE.bazel.lock in the mirror should be a copy of the live one (%v)", err)
	}
	write(t, mirrorLock, "rewritten by Bazel\n")
	if got := read(filepath.Join(ws, "MODULE.bazel.lock")); got != "live lock\n" {
		t.Errorf("real lock file changed to %q", got)
	}

	// Entries added to or removed from the workspace show up next time.
	write(t, filepath.Join(ws, "added", "BUILD"), "")
	os.RemoveAll(filepath.Join(ws, "other"))
	if _, err := w.Checkout(ctx, "base", old); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(co.Root, "added", "BUILD")); err != nil {
		t.Errorf("new workspace entry not mirrored: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(co.Root, "other")); err == nil {
		t.Error("removed workspace entry still mirrored")
	}
}

// gitHead returns the commit checked out in dir.
func gitHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:len(out)-1])
}
