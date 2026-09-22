#!/usr/bin/env bash
set -euo pipefail
version=${1:?Usage: build-release.sh VERSION ARCH [OUTPUT_DIR]}
arch=${2:?Missing architecture}
out=${3:-dist}
[[ $version =~ ^[A-Za-z0-9._-]+$ ]] || { echo 'Invalid version' >&2; exit 1; }
case $arch in amd64|arm64) ;; *) echo 'Unsupported architecture' >&2; exit 1;; esac
mkdir -p "$out"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
name="doubletake-${version}-linux-${arch}"
mkdir -p "$stage/$name/man/man1"
for command in doubletake doubletake-ctl doubletake-test-receiver; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -mod=readonly -trimpath -ldflags='-s -w' -o "$stage/$name/$command" "./cmd/$command"
  cp "man/man1/$command.1" "$stage/$name/man/man1/"
done
cp README.md FORK.md LICENSE COPYING.GPL "$stage/$name/"
printf '%s\n' "$version" > "$stage/$name/VERSION"
git rev-parse HEAD > "$stage/$name/REVISION"
tar -czf "$out/$name.tar.gz" -C "$stage" "$name"
(cd "$out" && sha256sum "$name.tar.gz" > "$name.tar.gz.sha256")
