// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Package bep reads failures out of a Bazel build event file written with
// --build_event_json_file. Shadow mode uses it to check a full CI run against
// the targets bazel-diff would have selected.
package bep

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/bjornleffler/bazel-diff/internal/label"
)

// Result is what a build event file says about the run.
type Result struct {
	// FailedTests had an overall status other than PASSED or FLAKY.
	FailedTests []string
	// FailedBuilds are targets that did not build.
	FailedBuilds []string
	// Tests is the number of distinct tests that reported a summary.
	Tests int
	// Targets is the number of distinct targets that completed or failed.
	Targets int
}

// event holds the fields of a build event that Parse uses.
type event struct {
	ID struct {
		TestSummary *struct {
			Label string `json:"label"`
		} `json:"testSummary"`
		TargetCompleted *struct {
			Label string `json:"label"`
		} `json:"targetCompleted"`
	} `json:"id"`
	TestSummary *struct {
		OverallStatus string `json:"overallStatus"`
	} `json:"testSummary"`
	Completed *struct {
		Success bool `json:"success"`
	} `json:"completed"`
	Aborted *json.RawMessage `json:"aborted"`
}

// Parse reads newline-delimited JSON build events.
func Parse(r io.Reader) (*Result, error) {
	failedTests := map[string]bool{}
	failedBuilds := map[string]bool{}
	tests := map[string]bool{}
	targets := map[string]bool{}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1<<20), 256<<20)
	for line := 1; scanner.Scan(); line++ {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("build event line %d: %w", line, err)
		}
		switch {
		case e.ID.TestSummary != nil && e.TestSummary != nil:
			l := label.Normalize(e.ID.TestSummary.Label)
			tests[l] = true
			if s := e.TestSummary.OverallStatus; s != "PASSED" && s != "FLAKY" {
				failedTests[l] = true
			}
		case e.ID.TargetCompleted != nil:
			l := label.Normalize(e.ID.TargetCompleted.Label)
			targets[l] = true
			if e.Aborted != nil || (e.Completed != nil && !e.Completed.Success) {
				failedBuilds[l] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return &Result{
		FailedTests:  sortedKeys(failedTests),
		FailedBuilds: sortedKeys(failedBuilds),
		Tests:        len(tests),
		Targets:      len(targets),
	}, nil
}

// sortedKeys returns a set's members in order.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
