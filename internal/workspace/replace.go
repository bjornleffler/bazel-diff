// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Local go.mod replace directives that point outside the repository.

package workspace

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// localReplaceTargets returns the workspace directories, relative to the
// workspace root in slash form, that the checkout's go.mod and go.work files
// replace modules with, such as dep for "replace example.com/dep => ../../dep"
// in repo/go/go.mod. Only targets inside the workspace but outside the
// checkout are returned, and only those that are directories in the live
// workspace at src: those are the ones the mirror would otherwise symlink.
// A target that contains the checkout is skipped, since copying it would
// mean copying the workspace around the repository.
func localReplaceTargets(src string, co Checkout) ([]string, error) {
	files, err := goModFiles(co.GitDir)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, file := range files {
		paths, err := localReplacePaths(file)
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			abs := filepath.Join(filepath.Dir(file), filepath.FromSlash(p))
			rel, err := filepath.Rel(co.Root, abs)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue // the workspace root itself, or outside the workspace
			}
			rel = filepath.ToSlash(rel)
			if rel == co.Prefix || strings.HasPrefix(rel, co.Prefix+"/") || strings.HasPrefix(co.Prefix, rel+"/") {
				continue // in the checkout, or containing it
			}
			if fi, err := os.Stat(filepath.Join(src, filepath.FromSlash(rel))); err != nil || !fi.IsDir() {
				continue
			}
			set[rel] = true
		}
	}
	targets := make([]string, 0, len(set))
	for t := range set {
		targets = append(targets, t)
	}
	sort.Strings(targets)
	return targets, nil
}

// goModFiles finds the go.mod and go.work files in the tree at dir, leaving
// out version control, vendor, testdata and node_modules directories, whose
// go.mod files no build reads, and directories whose names start with a dot
// or an underscore, which the go command ignores.
func goModFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != dir && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if (name == "go.mod" || name == "go.work") && d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// localReplacePaths returns the local directory paths on the right-hand side
// of the replace directives in the go.mod or go.work file at path: those
// starting with ./ or ../, which are relative to the file's directory. Both
// the single-line form and the parenthesised block form are read.
func localReplacePaths(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var paths []string
	inBlock := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			continue
		case inBlock && fields[0] == ")":
			inBlock = false
			continue
		case inBlock:
		case fields[0] == "replace" && len(fields) == 2 && fields[1] == "(":
			inBlock = true
			continue
		case fields[0] == "replace":
			fields = fields[1:]
		default:
			continue
		}
		if p, ok := replacementDir(fields); ok {
			paths = append(paths, p)
		}
	}
	return paths, scanner.Err()
}

// replacementDir picks the replacement out of the fields of one replace
// directive (without the "replace" keyword) and reports whether it is a
// relative directory. A directory replacement is the single field after
// "=>"; a module replacement has a version after it.
func replacementDir(fields []string) (string, bool) {
	for i, f := range fields {
		if f != "=>" {
			continue
		}
		rest := fields[i+1:]
		if len(rest) != 1 {
			return "", false
		}
		p := strings.Trim(rest[0], "\"`")
		if p == "." || p == ".." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
			return p, true
		}
		return "", false
	}
	return "", false
}
