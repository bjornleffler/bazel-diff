// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package snapshot defines the record of one revision's target hashes, which
// bazel-diff2 writes to disk, caches per commit, and compares between revisions.
package snapshot

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SchemaVersion changes whenever the hashing scheme or the file format
// changes. Snapshots with different schema versions are never compared.
const SchemaVersion = 1

// Kinds of targets that are not rules. A rule target's Kind is its rule class.
const (
	KindSourceFile       = "source file"
	KindGeneratedFile    = "generated file"
	KindPackageGroup     = "package group"
	KindEnvironmentGroup = "environment group"
)

// BazelReleaseSeed is the key under which Seeds records the Bazel release, so
// a Bazel upgrade is treated like any other seed change.
const BazelReleaseSeed = "@bazel_release"

// Snapshot holds every target's hash at one revision, plus what is needed to
// decide whether two snapshots can be compared at all.
type Snapshot struct {
	SchemaVersion int    `json:"schema_version"`
	ToolVersion   string `json:"tool_version"`
	// Commit is the git commit the working tree was at. When Dirty is set the
	// tree had local changes, so the snapshot must not be cached under Commit.
	Commit string `json:"commit"`
	Dirty  bool   `json:"dirty"`
	// Fingerprint identifies the settings the snapshot was made with: the
	// query expression and flags, the startup flags and the seed patterns.
	Fingerprint  string `json:"fingerprint"`
	BazelRelease string `json:"bazel_release"`
	// Seeds maps each seed file to its content hash. A change to any seed
	// invalidates every target, so it forces a full build.
	Seeds   map[string]string `json:"seeds"`
	Targets []Target          `json:"targets"`
}

// Target is one node of the dependency graph.
type Target struct {
	// Label is normalized: "//pkg:name" in the main repository and
	// "@@canonical_repo//pkg:name" in external ones.
	Label string   `json:"l"`
	Kind  string   `json:"k"`
	Tags  []string `json:"t,omitempty"`
	// Self hashes the target's own definition and content, without its
	// dependencies. Hash also covers the Hash of every dependency, so a target
	// whose Hash changed but whose Self did not was impacted only indirectly.
	Self string `json:"s"`
	Hash string `json:"h"`
	// Deps indexes into Snapshot.Targets.
	Deps []int32 `json:"d,omitempty"`
}

// IsRule reports whether the target is a rule rather than a file or group.
func (t *Target) IsRule() bool {
	switch t.Kind {
	case KindSourceFile, KindGeneratedFile, KindPackageGroup, KindEnvironmentGroup:
		return false
	}
	return true
}

// IsTest reports whether the target is a test rule. Bazel requires every test
// rule class to end in "_test"; test_suite is not a test rule.
func (t *Target) IsTest() bool {
	return t.IsRule() && strings.HasSuffix(t.Kind, "_test")
}

// IsExternal reports whether the target lives in an external repository.
func (t *Target) IsExternal() bool {
	return strings.HasPrefix(t.Label, "@")
}

// HasTag reports whether the target carries the given tag.
func (t *Target) HasTag(tag string) bool {
	for _, have := range t.Tags {
		if have == tag {
			return true
		}
	}
	return false
}

// Index maps every label to its position in Targets.
func (s *Snapshot) Index() map[string]int {
	index := make(map[string]int, len(s.Targets))
	for i := range s.Targets {
		index[s.Targets[i].Label] = i
	}
	return index
}

// Write stores the snapshot as JSON, gzip-compressed when path ends in ".gz".
// It writes a temporary file and renames it, so readers never see half a file.
func Write(path string, s *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	buffered := bufio.NewWriterSize(tmp, 1<<20)
	var w io.Writer = buffered
	var zw *gzip.Writer
	if strings.HasSuffix(path, ".gz") {
		zw = gzip.NewWriter(buffered)
		w = zw
	}
	if err := json.NewEncoder(w).Encode(s); err != nil {
		tmp.Close()
		return fmt.Errorf("encoding snapshot: %w", err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := buffered.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Read loads a snapshot written by Write. It detects gzip by content, not by
// file name, so a cache download under any name still reads.
func Read(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 1<<20)
	var r io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		defer zr.Close()
		r = zr
	}
	var s Snapshot
	if err := json.NewDecoder(r).Decode(&s); err != nil {
		return nil, fmt.Errorf("decoding snapshot %s: %w", path, err)
	}
	for i := range s.Targets {
		for _, d := range s.Targets[i].Deps {
			if d < 0 || int(d) >= len(s.Targets) {
				return nil, fmt.Errorf("snapshot %s: target %s has out-of-range dependency %d", path, s.Targets[i].Label, d)
			}
		}
	}
	return &s, nil
}
