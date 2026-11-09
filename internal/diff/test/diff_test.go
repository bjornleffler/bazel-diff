// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Tests for the diff package.

package test

import (
	"testing"

	"github.com/bjornleffler/bazel-diff/internal/diff"
	"github.com/bjornleffler/bazel-diff/internal/snapshot"
)

// snap builds a snapshot from targets with the given seeds.
func snap(targets []snapshot.Target) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		SchemaVersion: snapshot.SchemaVersion,
		Fingerprint:   "fp",
		Seeds:         map[string]string{".bazelrc": "rc1"},
		Targets:       targets,
	}
}

// chain is //a:test -> //b:lib -> //c:src.cc, plus an unrelated //d:lib.
func chain(srcSelf, srcHash, libHash, testHash string) []snapshot.Target {
	return []snapshot.Target{
		{Label: "//a:test", Kind: "cc_test", Self: "t", Hash: testHash, Deps: []int32{1}},
		{Label: "//b:lib", Kind: "cc_library", Self: "l", Hash: libHash, Deps: []int32{2}},
		{Label: "//c:src.cc", Kind: snapshot.KindSourceFile, Self: srcSelf, Hash: srcHash},
		{Label: "//d:lib", Kind: "cc_library", Self: "d", Hash: "d"},
	}
}

// TestDistanceAndCause checks direct and transitive impacts.
func TestDistanceAndCause(t *testing.T) {
	base := snap(chain("s1", "s1", "l1", "t1"))
	head := snap(chain("s2", "s2", "l2", "t2"))
	r := diff.Compare(base, head)
	if r.FullBuild {
		t.Fatalf("unexpected full build: %v", r.Reasons)
	}
	want := map[string]struct {
		direct   bool
		distance int
		via      string
	}{
		"//a:test":   {false, 2, "//b:lib"},
		"//b:lib":    {false, 1, "//c:src.cc"},
		"//c:src.cc": {true, 0, ""},
	}
	if len(r.Impacted) != len(want) {
		t.Fatalf("impacted = %+v", r.Impacted)
	}
	for _, imp := range r.Impacted {
		w := want[imp.Label]
		if imp.Direct != w.direct || imp.Distance != w.distance || imp.Via != w.via || imp.Cause != "//c:src.cc" {
			t.Errorf("%s = %+v, want %+v", imp.Label, imp, w)
		}
	}
	if !r.Impacted[0].Test {
		t.Error("//a:test not marked as a test")
	}
}

// TestNewAndDeleted adds one target and removes another.
func TestNewAndDeleted(t *testing.T) {
	base := snap(chain("s", "s", "l", "t"))
	headTargets := chain("s", "s", "l", "t")[:3]
	headTargets = append(headTargets, snapshot.Target{Label: "//e:new", Kind: "cc_library", Self: "n", Hash: "n"})
	r := diff.Compare(base, snap(headTargets))
	if len(r.Impacted) != 1 || r.Impacted[0].Label != "//e:new" || !r.Impacted[0].New || !r.Impacted[0].Direct {
		t.Errorf("impacted = %+v", r.Impacted)
	}
	if len(r.Deleted) != 1 || r.Deleted[0] != "//d:lib" {
		t.Errorf("deleted = %v", r.Deleted)
	}
}

// TestSeedChangeForcesFullBuild changes a seed file.
func TestSeedChangeForcesFullBuild(t *testing.T) {
	base := snap(chain("s", "s", "l", "t"))
	head := snap(chain("s", "s", "l", "t"))
	head.Seeds = map[string]string{".bazelrc": "rc2", "MODULE.bazel": "m"}
	r := diff.Compare(base, head)
	if !r.FullBuild || len(r.Reasons) != 2 {
		t.Errorf("full=%v reasons=%v", r.FullBuild, r.Reasons)
	}
}

// TestFingerprintMismatch refuses to scope snapshots made differently.
func TestFingerprintMismatch(t *testing.T) {
	base := snap(chain("s", "s", "l", "t"))
	head := snap(chain("s", "s", "l", "t"))
	head.Fingerprint = "other"
	if r := diff.Compare(base, head); !r.FullBuild {
		t.Error("fingerprint mismatch did not force a full build")
	}
}
