// Copyright 2026 Bjorn Leffler.
// All rights reserved.

// Tests for the bep package.

package test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bjornleffler/bazel-diff2/internal/bep"
)

// events is a trimmed build event file.
const events = `{"id":{"started":{}},"started":{"uuid":"x"}}
{"id":{"targetCompleted":{"label":"//a:lib","configuration":{"id":"c"}}},"completed":{"success":true}}
{"id":{"targetCompleted":{"label":"//a:broken","configuration":{"id":"c"}}},"completed":{"success":false}}
{"id":{"targetCompleted":{"label":"@@//b:aborted","configuration":{"id":"c"}}},"aborted":{"reason":"ANALYSIS_FAILURE"}}
{"id":{"testSummary":{"label":"//a:ok_test","configuration":{"id":"c"}}},"testSummary":{"overallStatus":"PASSED"}}
{"id":{"testSummary":{"label":"//a:flaky_test","configuration":{"id":"c"}}},"testSummary":{"overallStatus":"FLAKY"}}
{"id":{"testSummary":{"label":"//a:bad_test","configuration":{"id":"c"}}},"testSummary":{"overallStatus":"FAILED"}}

`

// TestParse extracts failures and counts.
func TestParse(t *testing.T) {
	r, err := bep.Parse(strings.NewReader(events))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.FailedTests, []string{"//a:bad_test"}) {
		t.Errorf("failed tests = %v", r.FailedTests)
	}
	if !reflect.DeepEqual(r.FailedBuilds, []string{"//a:broken", "//b:aborted"}) {
		t.Errorf("failed builds = %v", r.FailedBuilds)
	}
	if r.Tests != 3 || r.Targets != 3 {
		t.Errorf("tests=%d targets=%d", r.Tests, r.Targets)
	}
	if _, err := bep.Parse(strings.NewReader("{not json\n")); err == nil {
		t.Error("parsed garbage")
	}
}
