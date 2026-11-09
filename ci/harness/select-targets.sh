#!/usr/bin/env bash
# Copyright 2026 Bjorn Leffler.
# SPDX-License-Identifier: Apache-2.0
#
# Selects the Bazel targets for one pipeline run and writes them to $OUT_DIR
# (see README.md, "Output files"). It never fails the pipeline: if bazel-diff
# errors, it writes a full-build selection instead, so a tool problem costs
# time, never test coverage.
#
# Environment:
#   BASE_REF          branch or commit the change merges into, e.g. origin/main (required)
#   PR_REFS           space-separated pull request heads; when set, runs queue mode
#   SNAPSHOT_CACHE    gs://..., s3://... or a directory holding base snapshots
#   OUT_DIR           output directory (default: bazel-diff-out)
#   BAZEL_DIFF        the bazel-diff binary (default: bazel-diff on PATH)
#   BAZEL_DIFF_ARGS   extra flags, whitespace-separated and unquoted, e.g.
#                     --fast-path=**/*.md --test-tag-filters=-flaky
#
# Afterwards, export the results as Harness output variables with:
#   set -a; . "${OUT_DIR}/outputs.env"; set +a
set -uo pipefail

readonly OUT_DIR="${OUT_DIR:-bazel-diff-out}"
readonly BAZEL_DIFF="${BAZEL_DIFF:-bazel-diff}"

# Writes a selection that builds and tests everything.
write_full_build() {
  local reason="$1"
  echo "select-targets: ${reason}; falling back to a full build" >&2
  mkdir -p "${OUT_DIR}"
  echo '//...' > "${OUT_DIR}/build_targets.txt"
  echo '//...' > "${OUT_DIR}/test_targets.txt"
  : > "${OUT_DIR}/deferred_test_targets.txt"
  cat > "${OUT_DIR}/outputs.env" <<ENV
FULL_BUILD=true
BUILD_COUNT=1
TEST_COUNT=1
DEFERRED_TEST_COUNT=0
IMPACTED_COUNT=0
BASE_COMMIT=
HEAD_COMMIT=$(git rev-parse HEAD 2>/dev/null)
ENV
}

# Makes BASE_REF resolvable in a CI clone that fetched only the change itself.
ensure_base_ref() {
  if git rev-parse --verify --quiet "${BASE_REF}^{commit}" > /dev/null; then
    return 0
  fi
  local branch="${BASE_REF#origin/}"
  echo "select-targets: fetching ${branch}" >&2
  git fetch --no-tags origin "+refs/heads/${branch}:refs/remotes/origin/${branch}"
}

# Runs bazel-diff for a single change or, with PR_REFS, a merge queue batch.
select_targets() {
  local -a extra=()
  read -r -a extra <<< "${BAZEL_DIFF_ARGS:-}"
  local -a common=(--out-dir "${OUT_DIR}")
  if [[ -n "${SNAPSHOT_CACHE:-}" ]]; then
    common+=(--cache "${SNAPSHOT_CACHE}")
  fi
  if [[ -n "${PR_REFS:-}" ]]; then
    local -a prs=()
    for ref in ${PR_REFS}; do
      prs+=(--pr "${ref}")
    done
    "${BAZEL_DIFF}" queue --base "${BASE_REF}" "${prs[@]}" "${common[@]}" ${extra[@]+"${extra[@]}"}
  else
    "${BAZEL_DIFF}" impacted --base "${BASE_REF}" "${common[@]}" ${extra[@]+"${extra[@]}"}
  fi
}

if [[ -z "${BASE_REF:-}" ]]; then
  write_full_build "BASE_REF is not set"
elif ! command -v "${BAZEL_DIFF}" > /dev/null; then
  write_full_build "${BAZEL_DIFF} is not installed"
elif ! ensure_base_ref; then
  write_full_build "cannot fetch ${BASE_REF}"
elif ! select_targets; then
  write_full_build "bazel-diff failed"
fi

cat "${OUT_DIR}/outputs.env"
