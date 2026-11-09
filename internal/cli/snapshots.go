// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Getting snapshots: from the cache when possible, otherwise by hashing a
// checkout.

package cli

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/bjornleffler/bazel-diff/internal/cache"
	"github.com/bjornleffler/bazel-diff/internal/git"
	"github.com/bjornleffler/bazel-diff/internal/snapshot"
	"github.com/bjornleffler/bazel-diff/internal/workspace"
)

// repoFlags locate the repository, the snapshot cache and the worktrees.
type repoFlags struct {
	workspace    string
	cacheURL     string
	upload       bool
	worktreeRoot string
}

// register adds the repository flags to fs.
func (r *repoFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&r.workspace, "workspace", ".", "directory inside the git checkout of the Bazel workspace")
	fs.StringVar(&r.cacheURL, "cache", "", "snapshot cache: a directory, gs://bucket/prefix or s3://bucket/prefix")
	fs.BoolVar(&r.upload, "upload", false, "upload snapshots this run computes for clean commits to --cache")
	fs.StringVar(&r.worktreeRoot, "worktree-root", "", "where to keep worktrees for other revisions (default: user cache dir)")
}

// snapshotter hands out snapshots for one repository and configuration.
type snapshotter struct {
	repo      git.Repo
	top       string
	cfg       workspace.Config
	store     cache.Store
	upload    bool
	worktrees workspace.Worktrees
}

// newSnapshotter resolves the repository flags.
func newSnapshotter(ctx context.Context, rf *repoFlags, cfg workspace.Config) (*snapshotter, error) {
	top, err := git.Repo{Dir: rf.workspace}.TopLevel(ctx)
	if err != nil {
		return nil, err
	}
	s := &snapshotter{repo: git.Repo{Dir: top}, top: top, cfg: cfg, upload: rf.upload}
	if rf.cacheURL != "" {
		if s.store, err = cache.Open(rf.cacheURL); err != nil {
			return nil, fmt.Errorf("%w: %v", errUsage, err)
		}
	} else if rf.upload {
		return nil, fmt.Errorf("%w: --upload needs --cache", errUsage)
	}
	root := rf.worktreeRoot
	if root == "" {
		if root, err = workspace.DefaultWorktreeRoot(top); err != nil {
			return nil, err
		}
	}
	s.worktrees = workspace.Worktrees{Repo: s.repo, Root: root}
	return s, nil
}

// fromCache returns the cached snapshot of commit, saved at dst, or nil when
// there is none or it cannot be used. Cache trouble is never fatal: the
// snapshot can always be computed instead.
func (s *snapshotter) fromCache(ctx context.Context, commit, dst string) *snapshot.Snapshot {
	if s.store == nil {
		return nil
	}
	key := cache.SnapshotKey(commit, s.cfg.Fingerprint())
	found, err := s.store.Get(ctx, key, dst)
	if err != nil {
		logf("warning: reading %s from cache %s: %v", key, s.store, err)
		return nil
	}
	if !found {
		logf("cache miss for %s", short(commit))
		return nil
	}
	snap, err := snapshot.Read(dst)
	if err != nil {
		logf("warning: ignoring unreadable cached snapshot: %v", err)
		return nil
	}
	if snap.Commit != commit || snap.Fingerprint != s.cfg.Fingerprint() {
		logf("warning: ignoring cached snapshot for %s: it records commit %s, fingerprint %s", short(commit), short(snap.Commit), snap.Fingerprint)
		return nil
	}
	logf("cache hit for %s", short(commit))
	return snap
}

// atCommit returns a snapshot of commit, from the cache or by hashing it in
// the worktree called name, and saves it at dst.
func (s *snapshotter) atCommit(ctx context.Context, name, commit, dst string) (*snapshot.Snapshot, error) {
	if snap := s.fromCache(ctx, commit, dst); snap != nil {
		return snap, nil
	}
	return s.computeAt(ctx, name, commit, dst)
}

// computeAt hashes commit in the worktree called name, without consulting
// the cache, and saves the snapshot at dst.
func (s *snapshotter) computeAt(ctx context.Context, name, commit, dst string) (*snapshot.Snapshot, error) {
	dir, err := s.worktrees.Checkout(ctx, name, commit)
	if err != nil {
		return nil, err
	}
	return s.hashDir(ctx, dir, dst, true)
}

// inPlace hashes the main checkout as it is on disk and saves it at dst.
func (s *snapshotter) inPlace(ctx context.Context, dst string) (*snapshot.Snapshot, error) {
	return s.hashDir(ctx, s.top, dst, true)
}

// hashDir hashes a checkout and saves the snapshot at dst. With upload it
// also stores the snapshot in the cache, if --upload asked for that and the
// checkout was clean; throwaway merge commits pass false.
func (s *snapshotter) hashDir(ctx context.Context, dir, dst string, upload bool) (*snapshot.Snapshot, error) {
	snap, err := workspace.Hash(ctx, dir, s.cfg)
	if err != nil {
		return nil, err
	}
	if err := snapshot.Write(dst, snap); err != nil {
		return nil, err
	}
	if upload {
		s.maybeUpload(ctx, snap, dst)
	}
	return snap, nil
}

// maybeUpload stores a snapshot in the cache when --upload is set. A dirty
// tree is never uploaded: its snapshot does not describe its commit.
func (s *snapshotter) maybeUpload(ctx context.Context, snap *snapshot.Snapshot, path string) {
	if !s.upload || s.store == nil {
		return
	}
	if snap.Dirty {
		logf("not uploading the snapshot of %s: the working tree had local changes", short(snap.Commit))
		return
	}
	key := cache.SnapshotKey(snap.Commit, snap.Fingerprint)
	if err := s.store.Put(ctx, key, path); err != nil {
		logf("warning: uploading %s to %s: %v", key, s.store, err)
		return
	}
	logf("uploaded %s to %s", key, s.store)
}

// resolve turns a revision into a commit SHA.
func (s *snapshotter) resolve(ctx context.Context, rev string) (string, error) {
	return s.repo.ResolveCommit(ctx, rev)
}

// outPath returns a path inside the output directory.
func outPath(outDir, name string) string {
	return filepath.Join(outDir, name)
}

// short abbreviates a commit SHA for messages.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
