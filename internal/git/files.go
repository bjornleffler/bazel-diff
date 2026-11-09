// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Content hashes for the files of a working tree.

package git

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Files maps repository-relative paths to content hashes.
type Files struct {
	blobs  map[string]string
	sorted []string
}

// NewFiles wraps a path-to-hash map.
func NewFiles(blobs map[string]string) *Files {
	sorted := make([]string, 0, len(blobs))
	for p := range blobs {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	return &Files{blobs: blobs, sorted: sorted}
}

// Paths returns every path, sorted.
func (f *Files) Paths() []string {
	return f.sorted
}

// Blob returns the hash of one file.
func (f *Files) Blob(path string) (string, bool) {
	h, ok := f.blobs[path]
	return h, ok
}

// FileHash returns the hash of a file or, for a directory, a hash over every
// file beneath it. Bazel allows a source directory as a target, and a change
// to anything inside it must change the target's hash.
func (f *Files) FileHash(path string) (string, bool) {
	if h, ok := f.blobs[path]; ok {
		return h, true
	}
	prefix := path + "/"
	if path == "" {
		prefix = ""
	}
	start := sort.SearchStrings(f.sorted, prefix)
	digest := sha256.New()
	found := false
	for _, p := range f.sorted[start:] {
		if !strings.HasPrefix(p, prefix) {
			break
		}
		found = true
		fmt.Fprintf(digest, "%s\x00%s\n", p[len(prefix):], f.blobs[p])
	}
	if !found {
		return "", false
	}
	return "dir:" + hex.EncodeToString(digest.Sum(nil)), true
}

// HashPath hashes a file or directory on disk. Files get their git blob ID, so
// the result matches what git would record; directories get a hash over every
// regular file beneath them. Symlinks are followed.
func HashPath(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if info.Mode().IsRegular() {
		h, err := blobHash(path, info.Size())
		return h, err == nil
	}
	if !info.IsDir() {
		return "", false
	}
	digest := sha256.New()
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// WalkDir does not follow symlinked directories; Stat resolves file
		// symlinks so their target's content counts.
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		h, err := blobHash(p, info.Size())
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		fmt.Fprintf(digest, "%s\x00%s\n", filepath.ToSlash(rel), h)
		return nil
	})
	if err != nil {
		return "", false
	}
	return "dir:" + hex.EncodeToString(digest.Sum(nil)), true
}

// blobHash computes the git blob ID of a file: sha1("blob <size>\0" + content).
func blobHash(path string, size int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", size)
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
