// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Tests for the repos package, against a fake output base.

package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bjornleffler/bazel-diff/internal/repos"
)

// layout builds an output base with one repository of each kind and returns
// the resolver and the repository cache directory.
func layout(t *testing.T) (*repos.Resolver, string) {
	t.Helper()
	root := t.TempDir()
	ob := filepath.Join(root, "output_base")
	ext := filepath.Join(ob, "external")
	install := filepath.Join(root, "install", "0123abcd")
	contents := filepath.Join(root, "cache", "repos", "v1", "contents", strings.Repeat("a", 64))
	local := filepath.Join(root, "local_module")
	for _, d := range []string{ext, filepath.Join(install, "embedded_tools"), filepath.Join(contents, "uuid-1"), local, filepath.Join(ext, "marked+")} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(contents, "uuid-1.recorded_inputs"), []byte("FILE:x 1\n"), 0o644)
	os.Symlink(filepath.Join(contents, "uuid-1"), filepath.Join(ext, "cached+"))
	os.Symlink(filepath.Join(install, "embedded_tools"), filepath.Join(ext, "bazel_tools"))
	os.Symlink(local, filepath.Join(ext, "local+"))
	os.WriteFile(filepath.Join(local, "f.txt"), []byte("v1"), 0o644)
	os.WriteFile(filepath.Join(ext, "@marked+.marker"), []byte("hash1\nENV:HOME /home/a\n"), 0o644)
	os.MkdirAll(filepath.Join(ext, "unmarked+"), 0o755)
	return &repos.Resolver{OutputBase: ob, InstallBase: install}, contents
}

// TestIdentities checks each repository kind.
func TestIdentities(t *testing.T) {
	r, _ := layout(t)
	for name, prefix := range map[string]string{
		"cached+":     "contents:" + strings.Repeat("a", 64),
		"bazel_tools": "install:0123abcd",
		"local+":      "local:",
		"marked+":     "marker:",
		"unmarked+":   "local:",
		"absent+":     "absent:",
	} {
		if got := r.RepoHash(name); !strings.HasPrefix(got, prefix) {
			t.Errorf("RepoHash(%s) = %q, want prefix %q", name, got, prefix)
		}
	}
	if _, ok := r.FileHash("cached+", "BUILD"); ok {
		t.Error("fetched repository files should be pinned by identity")
	}
}

// TestLocalFilesHashedFromDisk changes a file in a local repository.
func TestLocalFilesHashedFromDisk(t *testing.T) {
	r, _ := layout(t)
	before, ok := r.FileHash("local+", "f.txt")
	if !ok {
		t.Fatal("local file not hashed")
	}
	target, _ := os.Readlink(filepath.Join(r.OutputBase, "external", "local+"))
	os.WriteFile(filepath.Join(target, "f.txt"), []byte("v2"), 0o644)
	if after, _ := r.FileHash("local+", "f.txt"); after == before {
		t.Error("local file hash did not change")
	}
	if h, _ := r.FileHash("local+", "missing.txt"); h != "missing" {
		t.Errorf("missing file hash = %q", h)
	}
}

// TestMarkerIgnore drops ignored marker lines, and the recorded inputs of a
// contents cache entry count.
func TestMarkerIgnore(t *testing.T) {
	a, contents := layout(t)
	plain := a.RepoHash("marked+")
	b := &repos.Resolver{OutputBase: a.OutputBase, InstallBase: a.InstallBase, IgnoreMarkerPrefixes: []string{"ENV:"}}
	if b.RepoHash("marked+") == plain {
		t.Error("ignoring ENV: lines did not change the identity")
	}
	cached := b.RepoHash("cached+")
	os.WriteFile(filepath.Join(contents, "uuid-1.recorded_inputs"), []byte("FILE:x 2\n"), 0o644)
	c := &repos.Resolver{OutputBase: a.OutputBase, InstallBase: a.InstallBase}
	if c.RepoHash("cached+") == cached {
		t.Error("recorded inputs do not affect the identity")
	}
}
