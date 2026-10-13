#!/usr/bin/env bash
# Cross-compile and package jitsi-stream for every supported OS/architecture.
# Pure Go (CGO_ENABLED=0): all targets build from any host, no C toolchain.
#
#   scripts/package.sh                 # all targets -> dist/
#   VERSION=v1.0.0 scripts/package.sh  # explicit version string
#   TARGETS="linux/amd64 windows/arm64" scripts/package.sh
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="${OUT:-dist}"
TARGETS="${TARGETS:-linux/amd64 linux/arm64 linux/armv7 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
NAME=jitsi-stream

rm -rf "$OUT"
mkdir -p "$OUT"
for target in $TARGETS; do
  os="${target%/*}"
  arch="${target#*/}"
  goarch="$arch"
  goarm=""
  if [[ "$arch" == armv7 ]]; then goarch=arm goarm=7; fi
  ext=""
  [[ "$os" == windows ]] && ext=".exe"
  pkg="${NAME}_${VERSION}_${os}_${arch}"
  dir="$OUT/$pkg"
  mkdir -p "$dir"
  for cmd in jitsi-stream jitsi-receive; do
    echo "build $os/$arch $cmd"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$goarch" GOARM="$goarm" go build -trimpath \
      -ldflags "-s -w -X main.version=$VERSION" -o "$dir/$cmd$ext" "./cmd/$cmd"
  done
  cp README.md LICENSE THIRD_PARTY.md "$dir/"
  cp third_party/NOTICES.txt "$dir/NOTICES.txt"
  mkdir -p "$dir/docs"
  cp docs/USAGE.md docs/BUILD.md "$dir/docs/"
  cp .env.example "$dir/env.example"
  (
    cd "$OUT"
    if [[ "$os" == windows ]]; then
      zip -qr "$pkg.zip" "$pkg"
    else
      tar -czf "$pkg.tar.gz" "$pkg"
    fi
  )
  rm -rf "$dir"
done

(
  cd "$OUT"
  if command -v sha256sum >/dev/null; then sha256sum ./*.tar.gz ./*.zip >SHA256SUMS; else shasum -a 256 ./*.tar.gz ./*.zip >SHA256SUMS; fi
)
echo "packages in $OUT/:"
ls -lh "$OUT"
