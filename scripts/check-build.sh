#!/bin/bash
# scripts/check-build.sh BINARY VERSION ARCH — what goreleaser runs after
# each build (.goreleaser.yaml): every module the binary records as
# compiled in has its licence under third_party/, and a binary for this
# machine's architecture reports VERSION.
set -euo pipefail
bin=$1 ver=$2 arch=$3
cd "$(dirname "$0")/.."
# captured first: a failing $(...) in a for list escapes set -e
mods=$(go version -m "$bin" | awk '$1 == "dep" {print $2}')
for m in $mods; do
	[ -d "third_party/$m" ] || { echo "check-build.sh: $m is compiled into $bin, but third_party/ has no licence for it" >&2; exit 1; }
done
[ "$arch" = "$(go env GOHOSTARCH)" ] || exit 0
got=$("$bin" --version)
got=${got%%$'\n'*} # not `| head -1`: the binary writes systemd's line after head exits, and pipefail fails on the SIGPIPE
case $got in # line 1 of printVersion (internal/cli/version.go)
"systemd-compose $ver" | "systemd-compose $ver "*) ;;
*) echo "check-build.sh: $bin says \"$got\", not $ver" >&2; exit 1 ;;
esac
