// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Real copies of workspace directories inside a mirror.

package workspace

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// syncTree makes dst a real copy of the directory src: directories and
// regular files are real, symlinks are copied as they are, and version
// control directories are left out (Go module zips leave them out too, and
// .git is often most of a checkout). src may itself be a symlink.
//
// The sync is incremental. A file whose size and modification time match
// the source is left alone, a copied file gets the source's modification
// time, and entries the source no longer has are removed. Unchanged files
// thus keep their timestamps between runs, which keeps Bazel from refetching
// a repository it watches. Files are never linked to the originals: a tool
// writing into the mirror must not write through to the live tree.
func syncTree(src, dst string) error {
	src, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != src && vcsDirs[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		seen[rel] = true
		return syncEntry(path, filepath.Join(dst, rel), d)
	})
	if err != nil {
		return err
	}
	return pruneTree(dst, seen)
}

// vcsDirs are the version control directories syncTree leaves out.
var vcsDirs = map[string]bool{".git": true, ".hg": true, ".svn": true, ".bzr": true}

// syncEntry brings the single entry dst in line with src, whose directory
// entry is d. Entries of a type syncTree does not copy (sockets, devices)
// are skipped.
func syncEntry(src, dst string, d fs.DirEntry) error {
	cur, curErr := os.Lstat(dst)
	switch {
	case d.IsDir():
		if curErr == nil && cur.IsDir() {
			return nil
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		return os.Mkdir(dst, 0o755)
	case d.Type()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if curErr == nil && cur.Mode()&fs.ModeSymlink != 0 {
			if old, err := os.Readlink(dst); err == nil && old == target {
				return nil
			}
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case d.Type().IsRegular():
		info, err := d.Info()
		if err != nil {
			return err
		}
		if curErr == nil && cur.Mode().IsRegular() && cur.Size() == info.Size() && cur.ModTime().Equal(info.ModTime()) {
			return nil
		}
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		return copyRegular(src, dst, info)
	}
	return nil
}

// copyRegular copies the regular file src, described by info, to the new
// file dst with the same permission bits and modification time.
func copyRegular(src, dst string, info fs.FileInfo) error {
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// pruneTree removes everything below dst whose path relative to dst is not
// in keep. It never follows symlinks.
func pruneTree(dst string, keep map[string]bool) error {
	return filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dst, path)
		if err != nil {
			return err
		}
		if keep[rel] {
			return nil
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}
