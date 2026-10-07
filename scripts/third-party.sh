#!/bin/bash
# scripts/third-party.sh — collect, under third_party/, the licence files of
# Go and of every module compiled into the linux amd64 and arm64 binaries.
# A binary carries its dependencies' LICENSE and NOTICE (Apache-2.0 4(a)
# and 4(d), BSD-3 clause 2), so each release archive ships them.
# goreleaser runs this before it builds (.goreleaser.yaml).
#
# Needs Go from go.dev/dl: a distribution's Go may not ship LICENSE and
# PATENTS in GOROOT.
set -euo pipefail
cd "$(dirname "$0")/.."
rm -rf third_party
mkdir -p third_party/go
goroot=$(go env GOROOT)
for f in LICENSE PATENTS; do
	[ -f "$goroot/$f" ] || { echo "third-party.sh: no $f in $goroot; use a toolchain from go.dev/dl" >&2; exit 1; }
	install -m 644 "$goroot/$f" third_party/go/
done
go mod download
for arch in amd64 arm64; do
	# captured first: a failure inside <(...) would escape set -e
	deps=$(CGO_ENABLED=0 GOOS=linux GOARCH=$arch go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' . | sort -u)
	while read -r path dir; do
		[ -n "$path" ] || continue
		files=$(cd "$dir" && ls | grep -E '^(LICEN[CS]E|NOTICE|COPYING)' || true)
		[ -n "$files" ] || { echo "third-party.sh: $path has no licence file in $dir" >&2; exit 1; }
		for f in $files; do install -D -m 644 "$dir/$f" "third_party/$path/$f"; done
	done <<<"$deps"
done
