// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Tests for the repos package, against a fake output base.

package test

import (
	"crypto/sha256"
	"encoding/hex"
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

// goEnvOutputBase builds an output base at root/name whose Gazelle-style
// go.env mentions its own path, plus a repository fetched into a shared
// contents cache entry that records the go.env hash Bazel would compute. It
// returns a resolver for that output base.
func goEnvOutputBase(t *testing.T, root, name, toolchain string) *repos.Resolver {
	t.Helper()
	ob := filepath.Join(root, name)
	cacheRepo := filepath.Join(ob, "external", "gazelle++cache")
	os.MkdirAll(cacheRepo, 0o755)
	goEnv := "GOPATH='" + cacheRepo + "'\nGOTOOLCHAIN='" + toolchain + "'\n"
	os.WriteFile(filepath.Join(cacheRepo, "go.env"), []byte(goEnv), 0o644)

	// Each fetch gets its own uuid entry under the same predeclared-input hash.
	contents := filepath.Join(root, "cache", "contents", strings.Repeat("b", 64))
	entry := filepath.Join(contents, name)
	os.MkdirAll(entry, 0o755)
	recorded := strings.Repeat("b", 64) + "\n" +
		"FILE:@@gazelle++cache//go.env " + sha256Hex(goEnv) + "\n" +
		"FILE:@@//go.mod 1234\n"
	os.WriteFile(entry+".recorded_inputs", []byte(recorded), 0o644)
	os.Symlink(entry, filepath.Join(ob, "external", "gazelle++go_deps+dep"))
	return &repos.Resolver{OutputBase: ob}
}

// sha256Hex returns the hex SHA-256 of s, as Bazel records file hashes.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestOutputBaseIndependent checks that a recorded input file which embeds
// the output base path does not make two output bases disagree, while a real
// change to that file still changes the identity.
func TestOutputBaseIndependent(t *testing.T) {
	root := t.TempDir()
	a := goEnvOutputBase(t, root, "ob_a", "local")
	b := goEnvOutputBase(t, root, "ob_b", "local")
	c := goEnvOutputBase(t, root, "ob_c", "go1.99")
	const dep = "gazelle++go_deps+dep"
	if ha, hb := a.RepoHash(dep), b.RepoHash(dep); ha != hb {
		t.Errorf("identities differ only by output base path:\n  %s\n  %s", ha, hb)
	}
	if a.RepoHash(dep) == c.RepoHash(dep) {
		t.Error("a real change to go.env did not change the identity")
	}
}
