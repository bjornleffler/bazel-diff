// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Package repos gives each external repository an identity hash, so a target
// in an external repository changes hash exactly when its repository does,
// without reading the repository's files.
//
// Bazel already tracks what defines each fetched repository, and leaves the
// evidence in its output base. For a repository at
// <output_base>/external/<name>:
//
//  1. A symlink into the repository contents cache
//     (<repo_cache>/contents/<predeclared-input-hash>/<uuid>) is identified by
//     that hash plus the recorded inputs file beside the entry. The hash covers
//     the repository rule, its attributes and its .bzl code, so a version bump
//     changes it. The uuid is random per fetch and is ignored.
//  2. A symlink into the install base (bazel_tools) is identified by the
//     install base, which changes with the Bazel binary.
//  3. Any other symlink points at local files (local_path_override,
//     local_repository): there is no fetch to record, so those files are
//     hashed from disk, one by one, as they are used.
//  4. A plain directory is identified by its marker file,
//     <output_base>/external/@<name>.marker, whose first line is the same
//     predeclared-input hash and whose remaining lines record the files,
//     environment variables and directories the fetch read.
//  5. A directory without a marker is treated like case 3.
package repos

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bjornleffler/bazel-diff/internal/git"
)

// contentsCacheEntry matches a repository contents cache entry path and
// captures its predeclared-input hash.
var contentsCacheEntry = regexp.MustCompile(`/contents/([0-9a-f]{64})/[^/]+$`)

// Resolver computes and memoizes repository identities. It is safe for
// concurrent use.
type Resolver struct {
	// OutputBase and InstallBase come from `bazel info`.
	OutputBase  string
	InstallBase string
	// IgnoreMarkerPrefixes drops marker lines with these prefixes before
	// hashing, for example "ENV:" when CI agents differ in irrelevant
	// environment variables that a toolchain configuration repository reads.
	IgnoreMarkerPrefixes []string
	// Warn reports problems that do not stop hashing. Optional.
	Warn func(format string, args ...any)

	mu    sync.Mutex
	repos map[string]*repo
}

// repo is what the Resolver learned about one repository.
type repo struct {
	identity string
	// local repositories have no identity that pins their contents, so their
	// files are hashed from disk at dir.
	local bool
	dir   string
}

// RepoHash returns the identity hash of a repository given its canonical
// name, such as "rules_cc+".
func (r *Resolver) RepoHash(name string) string {
	return r.lookup(name).identity
}

// FileHash returns the content hash of a file in a local repository. For
// fetched repositories it returns ok=false: their identity already pins every
// file, so reading them would only cost time.
func (r *Resolver) FileHash(name, path string) (string, bool) {
	info := r.lookup(name)
	if !info.local {
		return "", false
	}
	if h, ok := git.HashPath(filepath.Join(info.dir, filepath.FromSlash(path))); ok {
		return h, true
	}
	return "missing", true
}

// lookup returns the memoized description of a repository, computing it on
// first use.
func (r *Resolver) lookup(name string) *repo {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.repos == nil {
		r.repos = make(map[string]*repo)
	}
	if info, ok := r.repos[name]; ok {
		return info
	}
	info := r.describe(name)
	r.repos[name] = info
	return info
}

// describe works out a repository's identity from the output base, following
// the cases listed in the package comment.
func (r *Resolver) describe(name string) *repo {
	dir := filepath.Join(r.OutputBase, "external", name)
	fi, err := os.Lstat(dir)
	if err != nil {
		r.warn("external repository %s is not in the output base (%v); its targets are hashed by definition only", name, err)
		return &repo{identity: "absent:" + name}
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(dir)
		if err != nil {
			r.warn("cannot resolve external repository %s: %v", name, err)
			return &repo{identity: "unresolved:" + name}
		}
		raw, _ := os.Readlink(dir)
		for _, candidate := range []string{raw, target} {
			if m := contentsCacheEntry.FindStringSubmatch(filepath.ToSlash(candidate)); m != nil {
				recorded, _ := os.ReadFile(candidate + ".recorded_inputs")
				return &repo{identity: "contents:" + m[1] + ":" + digest(r.filterMarker(recorded))}
			}
		}
		if r.InstallBase != "" {
			// target is fully resolved, so compare it with the resolved install
			// base too (on macOS, /var is a symlink to /private/var).
			installBase := r.InstallBase
			if resolved, err := filepath.EvalSymlinks(installBase); err == nil {
				installBase = resolved
			}
			if within(target, installBase) || within(raw, r.InstallBase) {
				return &repo{identity: "install:" + filepath.Base(r.InstallBase)}
			}
		}
		return &repo{identity: "local:" + name, local: true, dir: target}
	}

	if marker, err := os.ReadFile(filepath.Join(r.OutputBase, "external", "@"+name+".marker")); err == nil {
		return &repo{identity: "marker:" + digest(r.filterMarker(marker))}
	}
	return &repo{identity: "local:" + name, local: true, dir: dir}
}

// filterMarker removes the lines that IgnoreMarkerPrefixes asks to ignore.
func (r *Resolver) filterMarker(content []byte) []byte {
	if len(r.IgnoreMarkerPrefixes) == 0 {
		return content
	}
	var kept []string
	for _, line := range strings.Split(string(content), "\n") {
		ignored := false
		for _, prefix := range r.IgnoreMarkerPrefixes {
			if strings.HasPrefix(line, prefix) {
				ignored = true
				break
			}
		}
		if !ignored {
			kept = append(kept, line)
		}
	}
	return []byte(strings.Join(kept, "\n"))
}

// warn forwards to Warn when it is set.
func (r *Resolver) warn(format string, args ...any) {
	if r.Warn != nil {
		r.Warn(format, args...)
	}
}

// digest returns the hex SHA-256 of b.
func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// within reports whether path is dir or lies beneath it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
