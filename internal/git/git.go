// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package git wraps the handful of git commands bazel-diff2 needs: resolving
// revisions, listing changed files, content hashes for every file in a working
// tree, and reusable worktrees for checking out other revisions.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo runs git commands in one working tree.
type Repo struct {
	Dir string
}

// identity is passed to commands that create commits, so they work on CI
// agents with no git user configured.
var identity = []string{"-c", "user.name=bazel-diff2", "-c", "user.email=bazel-diff2@localhost"}

// run executes git with the given arguments and returns its standard output.
// The error includes git's standard error, which is where it explains itself.
func (r Repo) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// output runs git and returns its trimmed standard output as a string.
func (r Repo) output(ctx context.Context, args ...string) (string, error) {
	out, err := r.run(ctx, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// TopLevel returns the absolute root of the working tree.
func (r Repo) TopLevel(ctx context.Context) (string, error) {
	return r.output(ctx, "rev-parse", "--show-toplevel")
}

// ResolveCommit turns any revision (branch, tag, ref or abbreviated SHA) into
// a full commit SHA.
func (r Repo) ResolveCommit(ctx context.Context, rev string) (string, error) {
	return r.output(ctx, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
}

// MergeBase returns the best common ancestor of two commits.
func (r Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	return r.output(ctx, "merge-base", a, b)
}

// IsDirty reports whether the working tree has staged, unstaged or untracked
// changes. Ignored files do not count.
func (r Repo) IsDirty(ctx context.Context) (bool, error) {
	out, err := r.run(ctx, nil, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	return len(out) > 0, err
}

// Change is one file that differs between two commits.
type Change struct {
	// Status is git's one-letter status: A (added), M (modified), D (deleted),
	// T (type changed). Renames are reported as a delete plus an add.
	Status byte
	Path   string
}

// ChangedFiles lists the files that differ between two commits.
func (r Repo) ChangedFiles(ctx context.Context, base, head string) ([]Change, error) {
	out, err := r.run(ctx, nil, "diff", "--name-status", "-z", "--no-renames", base, head, "--")
	if err != nil {
		return nil, err
	}
	fields := splitNUL(out)
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("git diff --name-status: odd number of fields")
	}
	changes := make([]Change, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		if fields[i] == "" {
			return nil, fmt.Errorf("git diff --name-status: empty status")
		}
		changes = append(changes, Change{Status: fields[i][0], Path: fields[i+1]})
	}
	return changes, nil
}

// splitNUL splits NUL-terminated git output into fields.
func splitNUL(out []byte) []string {
	s := strings.TrimSuffix(string(out), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// WorkingTreeFiles returns a content hash for every file git knows about in
// the working tree, as it is on disk right now. Unchanged files take their
// hash from the index, which is free; only modified and untracked files are
// read. The hashes are git blob IDs, so a clean tree hashes the same as the
// commit it is checked out at, whichever machine it is on. It also reports
// whether the tree was dirty.
func (r Repo) WorkingTreeFiles(ctx context.Context) (*Files, bool, error) {
	out, err := r.run(ctx, nil, "ls-files", "-s", "-z")
	if err != nil {
		return nil, false, err
	}
	blobs := make(map[string]string)
	for _, entry := range splitNUL(out) {
		// "<mode> <object> <stage>\t<path>"
		tab := strings.IndexByte(entry, '\t')
		meta := strings.Fields(entry[:max(tab, 0)])
		if tab < 0 || len(meta) != 3 {
			return nil, false, fmt.Errorf("git ls-files -s: unexpected entry %q", entry)
		}
		blobs[entry[tab+1:]] = meta[1]
	}

	out, err = r.run(ctx, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, false, err
	}
	statuses := splitNUL(out)
	var rehash []string
	for _, entry := range statuses {
		// "XY <path>": X is the index status, Y the working tree status.
		if len(entry) < 4 {
			return nil, false, fmt.Errorf("git status: unexpected entry %q", entry)
		}
		x, y, path := entry[0], entry[1], entry[3:]
		switch {
		case x == '?' && y == '?':
			rehash = append(rehash, path)
		case y == 'D' || (x == 'D' && y == ' '):
			delete(blobs, path)
		case y != ' ':
			rehash = append(rehash, path)
		}
	}
	if err := r.hashFromDisk(ctx, rehash, blobs); err != nil {
		return nil, false, err
	}
	return NewFiles(blobs), len(statuses) > 0, nil
}

// hashFromDisk computes git blob IDs for files as they are on disk and stores
// them in blobs. Paths that are gone or are not regular files are dropped.
func (r Repo) hashFromDisk(ctx context.Context, paths []string, blobs map[string]string) error {
	var stdin bytes.Buffer
	var kept []string
	for _, p := range paths {
		info, err := os.Stat(filepath.Join(r.Dir, p))
		if err != nil || !info.Mode().IsRegular() || strings.ContainsAny(p, "\n\r") {
			delete(blobs, p)
			continue
		}
		kept = append(kept, p)
		stdin.WriteString(p)
		stdin.WriteByte('\n')
	}
	if len(kept) == 0 {
		return nil
	}
	out, err := r.run(ctx, stdin.Bytes(), "hash-object", "--stdin-paths")
	if err != nil {
		return err
	}
	ids := strings.Fields(string(out))
	if len(ids) != len(kept) {
		return fmt.Errorf("git hash-object: got %d hashes for %d paths", len(ids), len(kept))
	}
	for i, p := range kept {
		blobs[p] = ids[i]
	}
	return nil
}

// Worktree checks out commit, detached, in a worktree at dir, creating the
// worktree on first use and reusing it afterwards. Reuse matters: Bazel keys
// its server and output base on the directory, so a stable directory keeps
// the analysis cache and fetched repositories warm between runs.
func (r Repo) Worktree(ctx context.Context, dir, commit string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		wt := Repo{Dir: dir}
		if _, err := wt.run(ctx, nil, "checkout", "--force", "--detach", commit); err == nil {
			// Drop untracked files left by an earlier revision, but keep ignored
			// ones such as a MODULE.bazel.lock the project chooses not to track:
			// deleting it would make Bazel re-resolve every module.
			_, err = wt.run(ctx, nil, "clean", "-ffd")
			return err
		}
		// The worktree is broken (for example its admin directory was pruned);
		// fall through and recreate it.
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if _, err := r.run(ctx, nil, "worktree", "prune"); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	_, err := r.run(ctx, nil, "worktree", "add", "--force", "--detach", dir, commit)
	return err
}

// Merge merges heads into base inside the worktree at dir, which must already
// exist, and returns the resulting commit. Several heads make an octopus
// merge. On a conflict the merge is aborted and an error names the heads.
func (r Repo) Merge(ctx context.Context, dir, base string, heads []string) (string, error) {
	if err := r.Worktree(ctx, dir, base); err != nil {
		return "", err
	}
	wt := Repo{Dir: dir}
	args := append(append([]string{}, identity...), "merge", "--no-ff", "--no-edit", "-m", "bazel-diff2 merge")
	args = append(args, heads...)
	if _, err := wt.run(ctx, nil, args...); err != nil {
		wt.run(ctx, nil, "merge", "--abort")
		return "", fmt.Errorf("merging %s into %s: %w", strings.Join(heads, " "), base, err)
	}
	return wt.output(ctx, "rev-parse", "HEAD")
}
