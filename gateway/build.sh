#!/usr/bin/env bash
#
# Build pms-gateway for the Pi inside Docker; Go is not installed on the laptop.
#
#   gateway/build.sh            vet + tests (native), then the linux/arm64 binary
#   gateway/build.sh --no-test  binary only
#   gateway/build.sh tidy       refresh go.mod and go.sum after changing imports
#
# Output: gateway/rootfs/usr/local/bin/pms-gateway, shipped by
# `platform/deploy.sh gateway`. Modules are verified against go.sum.

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="${GO_IMAGE:-golang:1.26-trixie}"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/pms-gateway-go"
mkdir -p "$CACHE"

# The whole repo is mounted so the tests can validate services/*/service.toml.
run() {
  docker run --rm --user "$(id -u):$(id -g)" \
    -v "$REPO:/repo" -v "$CACHE:/cache" -w /repo/gateway \
    -e HOME=/tmp -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod \
    -e GOTOOLCHAIN=local -e CGO_ENABLED=0 \
    "$IMAGE" "$@"
}

case "${1:-}" in
  tidy)      run go mod tidy; exit 0 ;;
  --no-test) ;;
  "")        run sh -c 'go vet ./... && go test ./...' ;;
  *)         sed -n '4,7p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac

run env GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w -buildid=" -o rootfs/usr/local/bin/pms-gateway .
ls -l "$REPO/gateway/rootfs/usr/local/bin/pms-gateway"
