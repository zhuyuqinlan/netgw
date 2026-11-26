#!/usr/bin/env bash
# build.sh 构建单个平台的二进制。
#
# 用法: build.sh <os> <arch> <version> <outfile>
set -euo pipefail

os="$1"; arch="$2"; version="$3"; outfile="$4"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

exe=""
if [ "$os" = "windows" ]; then exe=".exe"; fi

CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
  go build -trimpath \
  -ldflags "-s -w -X main.version=$version" \
  -o "$outfile" ./cmd/gateway

echo "build: $outfile ($os/$arch, version=$version)"
