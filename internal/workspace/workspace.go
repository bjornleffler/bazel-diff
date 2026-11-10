// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Package workspace hashes a Bazel workspace into a snapshot, and manages the
// git worktrees used to hash revisions other than the one checked out.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bjornleffler/bazel-diff/internal/bazel"
	"github.com/bjornleffler/bazel-diff/internal/buildpb"
	"github.com/bjornleffler/bazel-diff/internal/git"
	"github.com/bjornleffler/bazel-diff/internal/glob"
	"github.com/bjornleffler/bazel-diff/internal/hasher"
	"github.com/bjornleffler/bazel-diff/internal/repos"
	"github.com/bjornleffler/bazel-diff/internal/snapshot"
)

// ToolVersion is recorded in every snapshot.
const ToolVersion = "0.1.0"

// DefaultQuery covers every target in the main repository and everything they
// depend on, external repositories included.
const DefaultQuery = "deps(//...:all-targets)"

// DefaultSeeds are files that change how Bazel builds everything, in ways the
// target graph does not show: flags, the Bazel version, and module
// declarations (MODULE.bazel registers toolchains, which `bazel query` does
// not see). A change to any of them forces a full build.
var DefaultSeeds = []string{
	"/.bazelversion",
	"/.bazeliskrc",
	"/.bazelrc",
	// Imported rc files can live anywhere.
	"*.bazelrc",
	".bazelrc.*",
	"/MODULE.bazel",
	"*.MODULE.bazel",
	"/tools/bazel",
}

// Config controls how a workspace is hashed. Base and head snapshots must use
// the same Config; Fingerprint makes sure they did.
type Config struct {
	Bazel        string
	StartupFlags []string
	Query        string
	QueryFlags   []string
	KeepGoing    bool
	Seeds        []string
	// RepoMarkerIgnore lists marker-line prefixes to drop when hashing
	// external repositories; see repos.Resolver.IgnoreMarkerPrefixes.
	RepoMarkerIgnore []string
	// Log receives progress messages and Bazel's own output.
	Log io.Writer
}

// DefaultConfig returns the settings used when no flags override them.
func DefaultConfig() Config {
	return Config{
		Query: DefaultQuery,
		Seeds: append([]string{}, DefaultSeeds...),
		Log:   os.Stderr,
	}
}

// Fingerprint identifies every setting that affects target hashes. Snapshots
// with different fingerprints are not comparable.
func (c Config) Fingerprint() string {
	h := sha256.New()
	fmt.Fprintf(h, "schema=%d\n", snapshot.SchemaVersion)
	fmt.Fprintf(h, "query=%s\n", c.Query)
	for _, list := range []struct {
		name   string
		values []string
	}{
		{"startup", c.StartupFlags},
		{"query_flag", c.QueryFlags},
		{"seed", c.Seeds},
		{"marker_ignore", c.RepoMarkerIgnore},
	} {
		for _, v := range list.values {
			fmt.Fprintf(h, "%s=%s\n", list.name, v)
		}
	}
	fmt.Fprintf(h, "keep_going=%t\n", c.KeepGoing)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// logf writes one progress line.
func (c Config) logf(format string, args ...any) {
	if c.Log != nil {
		fmt.Fprintf(c.Log, "bazel-diff: "+format+"\n", args...)
	}
}

// Hash hashes a checkout as it is on disk.
func Hash(ctx context.Context, co Checkout, cfg Config) (*snapshot.Snapshot, error) {
	start := time.Now()
	repo := git.Repo{Dir: co.GitDir}
	commit, err := repo.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	seeds, err := glob.CompileAll(cfg.Seeds)
	if err != nil {
		return nil, err
	}
	// Read file hashes before Bazel runs: Bazel may rewrite MODULE.bazel.lock,
	// which must not make the tree look dirty.
	files, dirty, err := repo.WorkingTreeFiles(ctx)
	if err != nil {
		return nil, err
	}
	cfg.logf("hashing %s at %s (%d files%s)", co.GitDir, short(commit), len(files.Paths()), dirtySuffix(dirty))

	runner := bazel.Runner{Bazel: cfg.Bazel, StartupFlags: cfg.StartupFlags, Dir: co.Root, Stderr: cfg.Log}
	info, err := runner.Info(ctx, "release", "output_base", "install_base")
	if err != nil {
		return nil, err
	}

	var targets []*buildpb.Target
	err = runner.Query(ctx, cfg.Query, cfg.QueryFlags, cfg.KeepGoing, func(t *buildpb.Target) error {
		targets = append(targets, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	cfg.logf("bazel query returned %d targets in %s", len(targets), time.Since(start).Round(time.Millisecond))

	resolver := &repos.Resolver{
		OutputBase:           info["output_base"],
		InstallBase:          info["install_base"],
		IgnoreMarkerPrefixes: cfg.RepoMarkerIgnore,
		Warn:                 func(format string, args ...any) { cfg.logf("warning: "+format, args...) },
	}
	hashed, err := hasher.Hash(targets, hasher.Options{
		Files: fileSource{files: files, co: co},
		Repos: resolver,
	})
	if err != nil {
		return nil, err
	}

	seedHashes := map[string]string{snapshot.BazelReleaseSeed: info["release"]}
	for _, p := range files.Paths() {
		if wp := co.WorkspacePath(p); seeds.Match(wp) {
			seedHashes[wp], _ = files.Blob(p)
		}
	}
	if co.Prefix != "" {
		hashRootSeeds(co, seeds, seedHashes)
	}
	cfg.logf("hashed %d targets at %s in %s", len(hashed), short(commit), time.Since(start).Round(time.Millisecond))
	return &snapshot.Snapshot{
		SchemaVersion: snapshot.SchemaVersion,
		ToolVersion:   ToolVersion,
		Commit:        commit,
		Dirty:         dirty,
		Fingerprint:   cfg.Fingerprint(),
		BazelRelease:  info["release"],
		Seeds:         seedHashes,
		Targets:       hashed,
	}, nil
}

// hashRootSeeds adds the seeds that sit in the workspace root outside the
// repository, such as a .bazelrc or a MODULE.bazel symlink into the
// repository, hashed from disk with symlinks followed. Only the root
// directory is scanned: the rest of the workspace belongs to other
// repositories, which are the same live files on both sides of a comparison.
func hashRootSeeds(co Checkout, seeds glob.Set, hashes map[string]string) {
	entries, err := os.ReadDir(co.Root)
	if err != nil {
		return
	}
	repoDir, _, _ := strings.Cut(co.Prefix, "/")
	for _, e := range entries {
		name := e.Name()
		if name == repoDir || !seeds.Match(name) {
			continue
		}
		if h, ok := git.HashPath(filepath.Join(co.Root, name)); ok {
			hashes[name] = h
		}
	}
}

// fileSource serves the hashes of the workspace-relative paths that targets
// name. Files in the repository come from git, falling back to the disk for
// files git ignores but a target still names; files outside the repository
// (in a workspace above it) are read from disk.
type fileSource struct {
	files *git.Files
	co    Checkout
}

// FileHash implements hasher.FileSource.
func (f fileSource) FileHash(path string) (string, bool) {
	if rp, ok := f.co.RepoPath(path); ok {
		if h, ok := f.files.FileHash(rp); ok {
			return h, true
		}
		return git.HashPath(filepath.Join(f.co.GitDir, filepath.FromSlash(rp)))
	}
	return git.HashPath(filepath.Join(f.co.Root, filepath.FromSlash(path)))
}

// short abbreviates a commit SHA for log messages.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

// dirtySuffix notes a dirty tree in log messages.
func dirtySuffix(dirty bool) string {
	if dirty {
		return ", working tree has local changes"
	}
	return ""
}

// Worktrees hands out reusable git worktrees under Root, one per name. When
// Layout puts the repository below its Bazel workspace, each worktree sits at
// the same prefix inside a mirror of that workspace, so Bazel runs there just
// as it does in the real one.
type Worktrees struct {
	Repo   git.Repo
	Root   string
	Layout Layout
}

// DefaultWorktreeRoot returns a per-repository directory in the user cache, so
// worktrees, and the Bazel output bases keyed on their paths, survive between
// runs on the same agent.
func DefaultWorktreeRoot(topLevel string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(topLevel))
	return filepath.Join(cache, "bazel-diff", "worktrees", hex.EncodeToString(sum[:])[:12]), nil
}

// Checkout checks commit out in the worktree called name.
func (w Worktrees) Checkout(ctx context.Context, name, commit string) (Checkout, error) {
	co := w.place(name)
	if err := w.Repo.Worktree(ctx, co.GitDir, commit); err != nil {
		return co, err
	}
	return co, w.refreshMirror(co)
}

// Merge merges heads into base in the worktree called name and returns the
// checkout and the merge commit.
func (w Worktrees) Merge(ctx context.Context, name, base string, heads []string) (Checkout, string, error) {
	co := w.place(name)
	commit, err := w.Repo.Merge(ctx, co.GitDir, base, heads)
	if err != nil {
		return co, commit, err
	}
	return co, commit, w.refreshMirror(co)
}

// place works out where the worktree called name goes.
func (w Worktrees) place(name string) Checkout {
	root := filepath.Join(w.Root, name)
	if w.Layout.Prefix == "" {
		return Checkout{GitDir: root, Layout: Layout{Root: root}}
	}
	return Checkout{
		GitDir: filepath.Join(root, filepath.FromSlash(w.Layout.Prefix)),
		Layout: Layout{Root: root, Prefix: w.Layout.Prefix},
	}
}

// refreshMirror rebuilds the mirror of the workspace around a checkout of a
// repository below its workspace; there is nothing to do when the checkout is
// the workspace. It runs after the checkout because the checked-out revision
// decides which workspace directories its go.mod files replace modules with.
func (w Worktrees) refreshMirror(co Checkout) error {
	if co.Prefix == "" {
		return nil
	}
	copies, err := localReplaceTargets(w.Layout.Root, co)
	if err != nil {
		return err
	}
	return mirror(w.Layout.Root, co.Root, co.Prefix, copies)
}

// SafeName turns a git ref into something usable as a directory name.
func SafeName(ref string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, ref)
}
