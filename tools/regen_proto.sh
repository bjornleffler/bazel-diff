#!/usr/bin/env bash
# Copyright 2026 Bjorn Leffler.
# SPDX-License-Identifier: Apache-2.0
#
# Regenerates internal/buildpb/build.pb.go from the vendored build.proto.
# The generated code is checked in, so Bazel never needs a protoc toolchain.
# Needs protoc and protoc-gen-go (go install google.golang.org/protobuf/cmd/protoc-gen-go).
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "${ROOT}/internal/buildpb"
protoc --go_out=. --go_opt=paths=source_relative build.proto
echo "regenerated ${ROOT}/internal/buildpb/build.pb.go"
