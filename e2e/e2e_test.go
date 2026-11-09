// Copyright 2026 Bjorn Leffler.
// SPDX-License-Identifier: Apache-2.0

// End-to-end tests: build a small Bazel workspace in a git repository, make a
// series of commits, and check the selection bazel-diff makes for each.
//
// They run real `bazel query` (never build or test), so they need Bazel on
// PATH and are skipped unless BAZEL_DIFF_E2E=1. Set BAZEL to choose the
// binary. Run them outside Bazel's sandbox:
//
//	BAZEL_DIFF_E2E=1 go test ./e2e/...
package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bjornleffler/bazel-diff/internal/cli"
	"github.com/bjornleffler/bazel-diff/internal/report"
)

// fixture is the initial workspace: file path to content.
var fixture = map[string]string{
	"MODULE.bazel": `module(name = "fixture")

bazel_dep(name = "extmod", version = "0.0.0")
local_path_override(module_name = "extmod", path = "third_party/extmod")
`,
	".bazelrc":         "# fixture\n",
	".bazelignore":     "third_party/extmod\n",
	"README.md":        "fixture\n",
	"defs/BUILD.bazel": "",
	"defs/defs.bzl": `def _impl(ctx):
    out = ctx.actions.declare_file(ctx.label.name + ".sh")
    ctx.actions.write(out, "exit 0", is_executable = True)
    return [DefaultInfo(executable = out)]

fixture_test = rule(
    implementation = _impl,
    test = True,
    attrs = {"data": attr.label_list(allow_files = True)},
)
`,
	"lib/BUILD.bazel": `filegroup(name = "lib", srcs = ["lib.txt"], visibility = ["//visibility:public"])
`,
	"lib/lib.txt": "lib v1\n",
	"app/BUILD.bazel": `load("//defs:defs.bzl", "fixture_test")

genrule(name = "app", srcs = ["//lib"], outs = ["app.out"], cmd = "cat $(SRCS) > $@")

fixture_test(name = "app_test", data = [":app"])
`,
	"other/BUILD.bazel": `load("//defs:defs.bzl", "fixture_test")

filegroup(name = "other", srcs = ["other.txt"])

fixture_test(name = "other_test", data = [":other"])
`,
	"other/other.txt": "other v1\n",
	"ext/BUILD.bazel": `load("//defs:defs.bzl", "fixture_test")

fixture_test(name = "ext_test", data = ["@extmod//:data"])
`,
	"third_party/extmod/MODULE.bazel": `module(name = "extmod", version = "0.0.0")
`,
	"third_party/extmod/BUILD.bazel": `filegroup(name = "data", srcs = ["data.txt"], visibility = ["//visibility:public"])
`,
	"third_party/extmod/data.txt": "ext v1\n",
}

// step is one commit and the selection expected for it.
type step struct {
	name      string
	edit      map[string]string
	fullBuild bool
	build     []string
	tests     []string
}

// steps are applied in order; each is compared with the one before.
var steps = []step{
	{
		name:  "source file edit",
		edit:  map[string]string{"lib/lib.txt": "lib v2\n"},
		build: []string{"//app:app", "//lib:lib"},
		tests: []string{"//app:app_test"},
	},
	{
		name:  "rule definition edit",
		edit:  map[string]string{"defs/defs.bzl": strings.Replace(fixture["defs/defs.bzl"], "exit 0", "exit 0 # v2", 1)},
		tests: []string{"//app:app_test", "//ext:ext_test", "//other:other_test"},
	},
	{
		name:  "local module edit",
		edit:  map[string]string{"third_party/extmod/data.txt": "ext v2\n"},
		tests: []string{"//ext:ext_test"},
	},
	{
		name: "rule moved within its BUILD file",
		edit: map[string]string{"other/BUILD.bazel": "# A comment that shifts every line.\n\n" + fixture["other/BUILD.bazel"]},
	},
	{
		name: "documentation only",
		edit: map[string]string{"README.md": "fixture v2\n"},
	},
	{
		name: "new manual test is not selected",
		edit: map[string]string{"other/BUILD.bazel": "# A comment that shifts every line.\n\n" + fixture["other/BUILD.bazel"] +
			"\nfixture_test(name = \"manual_test\", data = [\":other\"], tags = [\"manual\"])\n"},
	},
	{
		name:      "seed file edit",
		edit:      map[string]string{".bazelrc": "# fixture v2\n"},
		fullBuild: true,
		build:     []string{"//..."},
		tests:     []string{"//..."},
	},
}

// TestImpacted walks through steps, running `impacted` after each commit.
func TestImpacted(t *testing.T) {
	bazel := requireBazel(t)
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	cacheDir := filepath.Join(root, "cache")
	worktrees := filepath.Join(root, "worktrees")
	git(t, root, "init", "-q", "-b", "main", ws)
	writeFiles(t, ws, fixture)
	commit(t, ws, "initial")
	defer shutdown(bazel, ws, filepath.Join(worktrees, "base"))

	common := []string{
		"--bazel", bazel,
		"--startup-flag=--max_idle_secs=120",
		"--workspace", ws,
		"--cache", cacheDir,
		"--upload",
		"--worktree-root", worktrees,
	}
	// Prime the cache with the initial commit.
	if code := cli.Main(context.Background(), append([]string{"hash", "--out", filepath.Join(root, "initial.json.gz")}, common[:len(common)-2]...)); code != 0 {
		t.Fatalf("hash exited %d", code)
	}

	for i, s := range steps {
		writeFiles(t, ws, s.edit)
		commit(t, ws, s.name)
		out := filepath.Join(root, "out", string(rune('a'+i)))
		args := append([]string{"impacted", "--base", "HEAD~1", "--out-dir", out}, common...)
		if code := cli.Main(context.Background(), args); code != 0 {
			t.Fatalf("%s: impacted exited %d", s.name, code)
		}
		summary := readEnv(t, filepath.Join(out, report.OutputsEnvFile))
		if got := summary["FULL_BUILD"] == "true"; got != s.fullBuild {
			t.Errorf("%s: FULL_BUILD=%s, want %v", s.name, summary["FULL_BUILD"], s.fullBuild)
		}
		if got := lines(t, filepath.Join(out, report.BuildTargetsFile)); !reflect.DeepEqual(got, s.build) {
			t.Errorf("%s: build = %v, want %v", s.name, got, s.build)
		}
		if got := lines(t, filepath.Join(out, report.TestTargetsFile)); !reflect.DeepEqual(got, s.tests) {
			t.Errorf("%s: tests = %v, want %v", s.name, got, s.tests)
		}
	}
}

// TestQueue merges two pull requests and checks the combined selection and
// its attribution.
func TestQueue(t *testing.T) {
	bazel := requireBazel(t)
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	worktrees := filepath.Join(root, "worktrees")
	git(t, root, "init", "-q", "-b", "main", ws)
	writeFiles(t, ws, fixture)
	commit(t, ws, "initial")
	defer shutdown(bazel, filepath.Join(worktrees, "base"), filepath.Join(worktrees, "merge"))

	git(t, ws, "checkout", "-q", "-b", "pr1", "main")
	writeFiles(t, ws, map[string]string{"lib/lib.txt": "lib v2\n"})
	commit(t, ws, "pr1")
	git(t, ws, "checkout", "-q", "-b", "pr2", "main")
	writeFiles(t, ws, map[string]string{"other/other.txt": "other v2\n"})
	commit(t, ws, "pr2")

	out := filepath.Join(root, "out")
	args := []string{"queue", "--base", "main", "--pr", "pr1", "--pr", "pr2", "--per-pr",
		"--bazel", bazel, "--startup-flag=--max_idle_secs=120",
		"--workspace", ws, "--worktree-root", worktrees, "--out-dir", out}
	if code := cli.Main(context.Background(), args); code != 0 {
		t.Fatalf("queue exited %d", code)
	}
	if got, want := lines(t, filepath.Join(out, report.TestTargetsFile)), []string{"//app:app_test", "//other:other_test"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tests = %v, want %v", got, want)
	}
	if got, want := lines(t, filepath.Join(out, "prs", "pr2", report.TestTargetsFile)), []string{"//other:other_test"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pr2 tests = %v, want %v", got, want)
	}
	b, err := os.ReadFile(filepath.Join(out, report.AttributionFile))
	if err != nil {
		t.Fatal(err)
	}
	var attribution map[string][]string
	if err := json.Unmarshal(b, &attribution); err != nil {
		t.Fatal(err)
	}
	if got := attribution["//app:app_test"]; !reflect.DeepEqual(got, []string{"pr1"}) {
		t.Errorf("//app:app_test attributed to %v, want [pr1]", got)
	}
	if got := attribution["//other:other_test"]; !reflect.DeepEqual(got, []string{"pr2"}) {
		t.Errorf("//other:other_test attributed to %v, want [pr2]", got)
	}
}

// requireBazel skips the test unless end-to-end tests are enabled, and
// returns the Bazel binary to use.
func requireBazel(t *testing.T) string {
	t.Helper()
	if os.Getenv("BAZEL_DIFF_E2E") != "1" {
		t.Skip("set BAZEL_DIFF_E2E=1 to run tests that invoke Bazel")
	}
	bazel := os.Getenv("BAZEL")
	if bazel == "" {
		bazel = "bazel"
	}
	if _, err := exec.LookPath(bazel); err != nil {
		t.Skipf("%s not found", bazel)
	}
	return bazel
}

// git runs a git command in dir.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// commit stages everything and commits it.
func commit(t *testing.T, dir, msg string) {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
}

// writeFiles writes files under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Bazel writes a lock file the fixture does not track.
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/MODULE.bazel.lock\n/bazel-*\n"), 0o644)
}

// lines reads a target file; nil when empty.
func lines(t *testing.T, path string) []string {
	t.Helper()
	l, err := report.ReadLines(path)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// readEnv parses outputs.env.
func readEnv(t *testing.T, path string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, l := range lines(t, path) {
		if k, v, ok := strings.Cut(l, "="); ok {
			env[k] = v
		}
	}
	return env
}

// shutdown stops the Bazel servers the test started.
func shutdown(bazel string, dirs ...string) {
	for _, dir := range dirs {
		cmd := exec.Command(bazel, "shutdown")
		cmd.Dir = dir
		cmd.Run()
	}
}
