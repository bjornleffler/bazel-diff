// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// Tests for the snapshot package.

package test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bjornleffler/bazel-diff/internal/snapshot"
)

// sample returns a small snapshot.
func sample() *snapshot.Snapshot {
	return &snapshot.Snapshot{
		SchemaVersion: snapshot.SchemaVersion,
		Commit:        "abc",
		Fingerprint:   "fp",
		Seeds:         map[string]string{".bazelrc": "x"},
		Targets: []snapshot.Target{
			{Label: "//a:a", Kind: "go_test", Tags: []string{"manual"}, Self: "s", Hash: "h", Deps: []int32{1}},
			{Label: "//a:a.go", Kind: snapshot.KindSourceFile, Self: "f", Hash: "f"},
		},
	}
}

// TestRoundTrip writes and reads plain and gzipped snapshots.
func TestRoundTrip(t *testing.T) {
	for _, name := range []string{"s.json", "s.json.gz"} {
		path := filepath.Join(t.TempDir(), name)
		if err := snapshot.Write(path, sample()); err != nil {
			t.Fatal(err)
		}
		got, err := snapshot.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, sample()) {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

// TestPredicates checks the target helpers.
func TestPredicates(t *testing.T) {
	s := sample()
	if !s.Targets[0].IsTest() || s.Targets[1].IsRule() || !s.Targets[0].HasTag("manual") {
		t.Error("predicates wrong")
	}
	if (&snapshot.Target{Label: "@@x+//:y", Kind: "test_suite"}).IsTest() {
		t.Error("test_suite counted as a test")
	}
	if s.Index()["//a:a.go"] != 1 {
		t.Error("index wrong")
	}
}

// TestRejectsBadDeps refuses a snapshot with dangling dependency indices.
func TestRejectsBadDeps(t *testing.T) {
	s := sample()
	s.Targets[0].Deps = []int32{7}
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := snapshot.Write(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Read(path); err == nil {
		t.Error("read a snapshot with a dangling dependency")
	}
	if _, err := snapshot.Read(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}
