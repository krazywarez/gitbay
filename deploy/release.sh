#!/bin/sh
# Build release binaries for a tag: reproducible cross-compiled gitbay and
# gitbayd with a checksum manifest.
#
#   git checkout v0.2.0 && ./deploy/release.sh v0.2.0
#
# Reproducibility: CGO off, -trimpath, stripped, empty build id; the VCS
# revision embedded by the toolchain is deterministic per commit. Anyone on
# the same Go toolchain and commit gets byte-identical binaries.
set -eu

V="${1:-}"
[ -n "$V" ] || { echo "usage: $0 <version-tag>" >&2; exit 2; }

out="dist/release/$V"
rm -rf "$out"
mkdir -p "$out"

for target in linux/amd64 linux/arm64 darwin/arm64; do
    goos="${target%/*}"
    goarch="${target#*/}"
    for bin in gitbay gitbayd; do
        name="${bin}-${V}-${goos}-${goarch}"
        echo "building $name"
        CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
            go build -trimpath -ldflags='-s -w -buildid=' \
            -o "$out/$name" "./cmd/$bin"
    done
done

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -- * > SHA256SUMS
else
    shasum -a 256 -- * > SHA256SUMS
fi
echo "wrote $out/SHA256SUMS"
cat SHA256SUMS
