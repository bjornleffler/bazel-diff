# bazel-diff

bazel-diff works out which Bazel targets a change affects, so CI builds and
tests those instead of `//...`. It works on one pull request or on several
merged together, and it is built for a Harness pipeline. The approach follows
[Tinder's bazel-diff](https://github.com/Tinder/bazel-diff): hash every
target at the base and head revisions, then compare the hashes.

Requirements: Bazel 8.4+ with bzlmod (WORKSPACE is not supported), git, and a
Linux or macOS CI agent.

## Quick start

1. Install bazel-diff
   - bazel build //cmd/bazel-diff
   - install -m 0755 bazel-bin/cmd/bazel-diff/bazel-diff_/bazel-diff ~/bin/

2. Analyse, build and test Bazel targets that have changed.
   - bazel-diff analyse ...
   - bazel-diff build ...
   - bazel-diff test ...

## How it works

1. **Query.** Run `bazel query 'deps(//...:all-targets)' --output=streamed_proto
   --consistent_labels --noproto:flatten_selects`. This returns every target
   in the repository and everything those targets depend on, external
   repositories included.
2. **Hash.** Give every target two hashes:
   - **Self** covers the target's own definition:
     - a rule's class, its attributes (including the full `select()`
       structure), its input labels, and the hash of the `.bzl` code that
       defines its rule class;
     - a source file's git blob ID;
     - a generated file's generating rule.
   - **Hash** covers Self plus the Hash of every dependency.

   Source locations and `generator_location` are left out, so moving a rule
   within its BUILD file changes nothing.
3. **External repositories** get an identity from evidence Bazel leaves in its
   output base. Their files are never read:
   - A repository in the repo contents cache is identified by its
     predeclared-input hash plus its recorded inputs.
   - A fetched repository is identified by its marker file.
   - `bazel_tools` is identified by the install base, which changes with the
     Bazel binary.
   - Local repositories, such as `local_path_override` modules, are the
     exception: their files are hashed from disk.

   Bumping a module version, or a `go.mod` entry that a module extension turns
   into a repository, changes only the targets that use that repository.
4. **Compare.** A target is impacted when its Hash differs from base, or when
   it is new. A breadth-first walk records each impacted target's distance
   from the change that caused it, which `explain` and `--max-test-distance`
   use.
5. **Seed files** affect everything in ways the target graph does not show.
   They are `.bazelversion`, `.bazelrc` (and any `*.bazelrc` or
   `.bazelrc.*`), `MODULE.bazel`, `*.MODULE.bazel`, `tools/bazel`, and the
   Bazel release. A change to any of them is a full build.

   `MODULE.bazel` is a seed because it registers toolchains, and `bazel query`
   cannot see toolchain resolution.
6. **Select.** Keep main-repository rules and drop:
   - files;
   - external targets;
   - `test_suite` targets, because their impacted members are already listed;
   - `manual` targets. Bazel builds a `manual` target whenever it is named
     explicitly, so they have to be dropped here.

   Apply the pipeline's tag filters, then split what's left into build targets
   and test targets.

### Base snapshots come from a cache

A pipeline on `main` hashes every commit and uploads the snapshot, keyed by
commit and settings fingerprint, to a directory, `gs://` or `s3://`. A pull
request pipeline then needs only one `bazel query`, for its own head. On a
cache miss the base is hashed in a git worktree that persists between runs,
so its Bazel server stays warm. Worktrees live under
`/tmp/bazel-diff/worktrees/`, one directory per repository path, on macOS,
Linux and CI agents alike; `--worktree-root` puts them elsewhere.

## Building

```bash
bazel build //...
bazel test //...
bazel run //cmd/bazel-diff -- version
```

The binary is `//cmd/bazel-diff`, built at
`bazel-bin/cmd/bazel-diff/bazel-diff_/bazel-diff`. It is a static Go binary,
so it copies into any CI image. To put it on `PATH`:

```bash
bazel build //cmd/bazel-diff
install -m 0755 bazel-bin/cmd/bazel-diff/bazel-diff_/bazel-diff ~/bin/
```

Use `install` rather than `cp`: Bazel's outputs are read-only, so `cp` cannot
overwrite an earlier copy, and on macOS overwriting a binary in place can get
it killed on its next run. `install` replaces the file instead.

## Commands

| Command | Use |
|---|---|
| `analyse [flags] <pattern>...` | No-op version of `build`: print the affected targets, split into those that would be built and those that would be tested, and build nothing. `analyze` is accepted too. |
| `build [flags] <pattern>... [-- <bazel flags>]` | Print the targets that your pending changes affect, restricted to the target patterns, then run `bazel build` on them. For local use: see "Building only what changed". |
| `test [flags] <pattern>... [-- <bazel flags>]` | Like `build`, but run `bazel test`: build the affected targets and run the affected tests. |
| `impacted --base <rev> [--head <rev>]` | Select targets for one change. Head defaults to the working tree as it is on disk, and base is the merge base of the two revisions. This is the usual CI entry point. |
| `queue --base <rev> --pr <rev> [--pr ...] [--per-pr]` | Merge the PRs into base in a worktree (an octopus merge) and select targets for the result. `--per-pr` also hashes each PR on its own and writes `attribution.json`, which maps each selected target to the PRs that cause it. Targets only the combination causes are listed as `(combination)`. |
| `hash [--rev <rev>] [--cache <url> --upload]` | Write a snapshot, and with `--upload` fill the cache. Run this on every commit to `main`. |
| `diff --base <file> --head <file>` | Select targets from two snapshot files. |
| `explain --target <label>` | Show the dependency chain from a selected target to the change that selected it. Reads the snapshots that `impacted` saves in its output directory. |
| `verify --bep <file>` | Shadow mode: compare a full run's failures with the selection. See "Rollout". |

Useful flags (run `bazel-diff <command> -h` for all of them):

- `--cache <url>`: where snapshots are cached. Add `--upload` to write as
  well as read.
- `--build-tag-filters`, `--test-tag-filters`: use the same values the
  pipeline passes to Bazel.
- `--fast-path <glob>` (repeatable): changed files that can be skipped without
  querying head, such as `**/*.md`. A file is skipped only if all of these are
  true:
  - it was modified, not added or deleted;
  - it is not a BUILD file, a `.bzl` file or a seed;
  - it is not a source target at base.

  **Never list files that a module extension reads**, such as `go.mod` or
  lock files.
- `--max-test-distance <n>`: defers tests more than `n` dependency edges from
  the change into `deferred_test_targets.txt`.
- `--parallel`: on a cache miss, hash base and head at the same time. This
  runs two Bazel servers, so it needs about twice the memory.
- `--query-flag`, `--startup-flag`, `--seed`, `--repo-marker-ignore=ENV:`:
  change how snapshots are made. They are part of the fingerprint, so base and
  head must use the same values. Use `--repo-marker-ignore=ENV:` only if CI
  agents differ in environment variables that a toolchain repository records.

### Building only what changed

```bash
bazel-diff build //...                      # what uncommitted changes affect
bazel-diff build --base main //...          # everything this branch changes
bazel-diff build //src/... -//src/gen/...   # restrict, as with bazel build
bazel-diff build //... -- --config=ci -k    # flags after -- go to bazel build
bazel-diff build --dry-run //...            # print the targets, do not build
bazel-diff analyse //...                    # the same, split into builds and tests
bazel-diff test //...                       # build, then run the affected tests
```

By default the change is the working tree compared with `HEAD`. `--base`
compares with the merge base of a revision instead. Patterns work as in Bazel:
absolute (`//...`, `//pkg/...`, `//pkg:all`, `//pkg:name`) or relative to the
current directory (`...`, `sub/...`, `:all`). A leading `-` excludes targets.
Put `bazel-diff` flags before the first pattern.

The command prints the affected targets on standard output, one per line, and
its progress on standard error. It then builds them, tests included, with
`bazel build --target_pattern_file`, and exits with Bazel's exit code.
`test` works the same way with `bazel test`, so flags after `--` go to
`bazel test`. If no tests are affected, it runs `bazel build` instead, because
`bazel test` fails when it finds no tests. Tests beyond
`--max-test-distance` are left out, since `bazel test` would run them.
`analyse` stops after printing. It lists the targets to build, then the tests
to run, with each heading on standard error, so standard output stays a plain
list. If nothing is affected, it does not run Bazel. If the change forces a
full build, such as a `.bazelversion` edit, it builds the patterns as given.

These commands query only what the patterns need: `deps(<patterns>)`, such as
`deps("//src/...")`. Nothing else can change a selected target, so the result
is the same as querying the whole repository, it is faster, and broken
packages outside the patterns do not get in the way. `--query` overrides it.
Because the query is part of the snapshot fingerprint, these snapshots are
not shared with a `--cache` filled by `hash`, which queries everything.

Hashing the base revision runs Bazel in a separate worktree. That worktree
uses the base's `.bazelversion`, so that Bazel version must be available.
bazelisk downloads it automatically.

### A workspace above the repository

The Bazel workspace can sit above the git repository: for example `~/git`
holds `MODULE.bazel`, `.bazelrc` and several repositories side by side, and
the repository's targets are `//bucketfs/...`. bazel-diff finds the workspace
root (the nearest directory with `MODULE.bazel`, `REPO.bazel`, `WORKSPACE` or
`WORKSPACE.bazel`) and adapts:

- The base revision is checked out inside a mirror of the workspace: every
  other entry of the workspace root is a symlink to the real one, symlinks
  (such as `MODULE.bazel -> bucketfs/MODULE.bazel.bucketfs`) are copied as
  they are so they resolve into the base checkout, and `MODULE.bazel.lock` is
  copied so Bazel's rewrite of it never touches the real one.
- A directory that one of the base revision's `go.mod` or `go.work` files
  replaces a module with, such as a sibling checkout named by
  `replace example.com/dep => ../../dep`, is a real copy in the mirror
  rather than a symlink (version control directories left out, refreshed
  incrementally on each run). Gazelle copies such a module with
  `filepath.Walk`, which does not enter a symlinked root, so a symlink would
  give an empty external repository and `BUILD file not found` errors for
  every package in the module.
- The default query of `impacted`, `hash` and `queue` covers the repository
  and its dependencies, `deps(//bucketfs/...:all-targets)`, not the whole
  workspace, whose other repositories may not even load. `build`, `test` and
  `analyse` query their patterns' dependencies, as everywhere.
- `--fast-path` globs and seeds match workspace paths, such as
  `bucketfs/docs/**`.

Only changes inside the repository are seen. Files outside it, such as other
repositories or a `.bazelrc` in the workspace root, have no base revision:
both sides read the same live files, so editing them selects nothing. A root
file that is a symlink into the repository does have one, so a change to
`MODULE.bazel.bucketfs` still forces a full build.

### Output files

`impacted`, `queue`, `diff`, `build`, `test` and `analyse` write these files to
`--out-dir`. The default is `bazel-diff-out`: at the workspace root for
`impacted`, `build`, `test` and `analyse`, whichever directory they run from,
and in the current directory for `queue` and `diff`:

| File | Content |
|---|---|
| `build_targets.txt` | Non-test rules, one per line, for `bazel build --target_pattern_file=...`. |
| `test_targets.txt` | Test rules, for `bazel test --target_pattern_file=...`. |
| `deferred_test_targets.txt` | Tests beyond `--max-test-distance`. |
| `outputs.env` | `FULL_BUILD`, `BUILD_COUNT`, `TEST_COUNT`, `DEFERRED_TEST_COUNT`, `IMPACTED_COUNT`, `BASE_COMMIT` and `HEAD_COMMIT`, as shell `KEY=value` lines. |
| `summary.json` | The counts, the reasons for a full build, whether the fast path ran, and how long the run took. |
| `impacted.json` | Every impacted target, with its kind, whether it changed directly, its distance, and its cause. |
| `base.json.gz`, `head.json.gz` | The two snapshots, for `explain` and for debugging. |

On a full build, both target files contain `//...` and `FULL_BUILD=true`.

## Harness

`ci/harness/` holds templates. Replace the `<...>` placeholders before use.

- `select-targets.sh` runs bazel-diff and **never fails the step**. If the
  tool errors, is missing, or cannot fetch the base, the script writes a
  full-build selection, so a tool problem costs time, not coverage.
  - It reads `BASE_REF`, `SNAPSHOT_CACHE` and `BAZEL_DIFF_ARGS` from the
    environment.
  - If `PR_REFS` is set, it runs `queue` mode instead.
- `pr-pipeline.yaml` runs the select step and exports `outputs.env` as output
  variables. The build and test steps then run with
  `--skip_incompatible_explicit_targets`, each only when its count is
  non-zero. Without that flag, an incompatible target named explicitly is an
  error.
- `post-merge-pipeline.yaml` hashes each commit on `main` into the cache. Pair
  it with a nightly full `bazel test //...`.

Clone with full history (`depth: 0`), or at least deep enough to reach the
merge base. Base and head snapshots must come from the same CI image. Toolchain
configuration repositories record environment details such as compiler paths,
so a snapshot from a different image looks like a toolchain change.

## Rollout

1. **Shadow mode.** Set the pipeline variable `shadow_mode=true`. Each PR
   still runs `bazel test //...` with a build event file. `bazel-diff verify`
   then writes `verify.json`, which lists every failing test or target the
   selection left out. Any miss is a bug to fix before going further. The
   other numbers in `verify.json` measure how much is saved.
2. **Enable.** Turn shadow mode off, and keep the nightly full build.
3. **Merge queue.** Use `queue` (set `PR_REFS`) for batched merges, with
   `--per-pr` to find the PR to evict when a batch fails.
4. **Tiering (optional).** Use `--max-test-distance` to run close tests before
   merge and deferred ones after. Do this only if the shadow-mode data shows
   that distant failures are rare.

## Limits

- **Undeclared inputs.** Tests that read files they don't declare, or that use
  the network, are invisible. The nightly full build catches them.
- **Toolchains.** Registration lives in `MODULE.bazel`, which is a seed. A
  change inside a toolchain repository is invisible unless some target depends
  on that repository directly.
- **Missing BUILD files.** A package that fails to load fails the run, unless
  `--keep-going` is set, which is not recommended: a target missing from the
  result looks deleted. The wrapper then falls back to a full build, which
  shows the real error.
- **`select()`.** Selections are configuration-agnostic: every branch counts,
  so the tool over-selects but never misses because of a `select()`.

## Development

```bash
bazel test //...                       # unit tests (in <package>/test/)
BAZEL_DIFF_E2E=1 go test ./e2e/...    # end-to-end, runs real `bazel query`
tools/regen_proto.sh                   # after updating internal/buildpb/build.proto
bazel run //:gazelle                   # after adding files or imports
```

`internal/buildpb/build.proto` is vendored from Bazel 8.4.2, without its
stardoc import. The generated `build.pb.go` is checked in, so building the
tool needs no protoc toolchain.
