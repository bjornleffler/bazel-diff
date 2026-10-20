// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Tests for the git package, against throwaway repositories.

package test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bjornleffler/bazel-diff2/internal/git"
)

// newRepo creates a repository with one commit and returns its path.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a.txt", "a")
	write(t, dir, "pkg/b.txt", "b")
	write(t, dir, "pkg/data/c.txt", "c")
	commit(t, dir, "first")
	return dir
}

// run runs git in dir and returns its trimmed output.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write creates a file, with parent directories.
func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	os.MkdirAll(filepath.Dir(full), 0o755)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// commit stages everything and commits it.
func commit(t *testing.T, dir, msg string) string {
	t.Helper()
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", msg)
	return run(t, dir, "rev-parse", "HEAD")
}

// TestWorkingTreeFiles covers clean, modified, untracked and deleted files.
func TestWorkingTreeFiles(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	repo := git.Repo{Dir: dir}
	clean, dirty, err := repo.WorkingTreeFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dirty {
		t.Error("fresh repository reported dirty")
	}
	if h, _ := clean.Blob("a.txt"); h != run(t, dir, "rev-parse", "HEAD:a.txt") {
		t.Errorf("a.txt hash %q is not the committed blob", h)
	}
	if h, ok := git.HashPath(filepath.Join(dir, "a.txt")); !ok || h != run(t, dir, "rev-parse", "HEAD:a.txt") {
		t.Errorf("HashPath(a.txt) = %q", h)
	}
	cleanDir, ok := clean.FileHash("pkg/data")
	if !ok {
		t.Fatal("no hash for directory pkg/data")
	}

	write(t, dir, "a.txt", "changed")
	write(t, dir, "pkg/data/new.txt", "new")
	os.Remove(filepath.Join(dir, "pkg/b.txt"))
	files, dirty, err := repo.WorkingTreeFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dirty {
		t.Error("modified repository reported clean")
	}
	if a, _ := files.Blob("a.txt"); a == "" || a == run(t, dir, "rev-parse", "HEAD:a.txt") {
		t.Errorf("modified a.txt hash = %q", a)
	}
	if _, ok := files.Blob("pkg/b.txt"); ok {
		t.Error("deleted pkg/b.txt still listed")
	}
	if _, ok := files.Blob("pkg/data/new.txt"); !ok {
		t.Error("untracked file not listed")
	}
	if d, _ := files.FileHash("pkg/data"); d == cleanDir {
		t.Error("directory hash did not change with a new file in it")
	}
}

// TestChangesWorktreeAndMerge covers diffing, worktree reuse and merging.
func TestChangesWorktreeAndMerge(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	repo := git.Repo{Dir: dir}
	base := run(t, dir, "rev-parse", "HEAD")

	run(t, dir, "checkout", "-q", "-b", "pr1")
	write(t, dir, "a.txt", "pr1")
	write(t, dir, "new.txt", "n")
	pr1 := commit(t, dir, "pr1")
	run(t, dir, "checkout", "-q", "-b", "pr2", base)
	os.Remove(filepath.Join(dir, "pkg/b.txt"))
	pr2 := commit(t, dir, "pr2")

	changes, err := repo.ChangedFiles(ctx, base, pr1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]byte{}
	for _, c := range changes {
		got[c.Path] = c.Status
	}
	if len(got) != 2 || got["a.txt"] != 'M' || got["new.txt"] != 'A' {
		t.Errorf("changes = %v", got)
	}
	if mb, err := repo.MergeBase(ctx, pr1, pr2); err != nil || mb != base {
		t.Errorf("merge base = %q, %v", mb, err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	for _, c := range []string{pr1, base, pr1} {
		if err := repo.Worktree(ctx, wt, c); err != nil {
			t.Fatal(err)
		}
		if head := run(t, wt, "rev-parse", "HEAD"); head != c {
			t.Errorf("worktree at %s, want %s", head, c)
		}
	}

	merged, err := repo.Merge(ctx, wt, base, []string{pr1, pr2})
	if err != nil {
		t.Fatal(err)
	}
	if parents := strings.Fields(run(t, wt, "rev-list", "--parents", "-n1", merged)); len(parents) != 4 {
		t.Errorf("octopus merge has parents %v", parents)
	}
	if _, err := os.Stat(filepath.Join(wt, "pkg/b.txt")); !os.IsNotExist(err) {
		t.Error("merge kept a file pr2 deleted")
	}

	// A conflicting pair must fail cleanly and leave the worktree usable.
	run(t, dir, "checkout", "-q", "-b", "pr3", base)
	write(t, dir, "a.txt", "pr3")
	pr3 := commit(t, dir, "pr3")
	if _, err := repo.Merge(ctx, wt, base, []string{pr1, pr3}); err == nil {
		t.Error("conflicting merge succeeded")
	}
	if _, err := repo.Merge(ctx, wt, base, []string{pr3}); err != nil {
		t.Errorf("merge after a conflict: %v", err)
	}
}
