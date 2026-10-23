#!/usr/bin/env bash
# Check the production Linux import graph, not every module in go.mod.
set -euo pipefail
cd "$(dirname "$0")/../server"
check_file=$(mktemp)
trap 'rm -f "$check_file"' EXIT
GOOS=linux CGO_ENABLED=0 go list -tags=zenith_minimal,nomsgpack -deps ./cmd/server > "$check_file"
if grep -E '^(cloud\.google\.com/|google\.golang\.org/(api|grpc)(/|$)|github\.com/ugorji/go/codec)' "$check_file"; then
  echo 'Minimal API links a dependency excluded by its contract.' >&2
  exit 1
fi
printf 'Minimal API dependency boundary passed.\n'
go test -tags=zenith_minimal,nomsgpack ./internal/auth ./internal/infra/cloudrun ./internal/handler ./cmd/server
