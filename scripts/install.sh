#!/bin/sh
# scripts/install.sh [VERSION] — install a release of systemd-compose:
# download its tarball and SHA256SUMS, check the tarball's sum, and put
# the binary in ~/.local/bin and the man page in ~/.local/share/man/man1.
#
#   curl -fsSL https://raw.githubusercontent.com/xflash96/systemd-compose/main/scripts/install.sh | sh
#   sh install.sh v0.1.0                   a given release; the latest otherwise
#   PREFIX=/usr/local sh install.sh        PREFIX/bin and PREFIX/share/man/man1
#
# Needs curl, tar and sha256sum.
set -eu
repo=xflash96/systemd-compose
prefix=${PREFIX:-$HOME/.local}
case $(uname -s) in
Linux) ;;
*) echo "install.sh: systemd-compose runs on Linux, not $(uname -s)" >&2; exit 1 ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) echo "install.sh: there is no release for $(uname -m)" >&2; exit 1 ;;
esac
ver=${1:-}
if [ -z "$ver" ]; then
	ver=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p')
	[ -n "$ver" ] || { echo "install.sh: could not find the latest release of $repo" >&2; exit 1; }
fi
name=systemd-compose_${ver#v}_linux_$arch
url=https://github.com/$repo/releases/download/$ver
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$name.tar.gz" "$url/$name.tar.gz"
curl -fsSL -o "$tmp/SHA256SUMS" "$url/SHA256SUMS"
(cd "$tmp" && grep " $name.tar.gz\$" SHA256SUMS | sha256sum -c --quiet -) ||
	{ echo "install.sh: $name.tar.gz does not match SHA256SUMS" >&2; exit 1; }
tar -C "$tmp" -xzf "$tmp/$name.tar.gz"
mkdir -p "$prefix/bin" "$prefix/share/man/man1"
cp "$tmp/$name/systemd-compose" "$prefix/bin/"
cp "$tmp/$name/docs/systemd-compose.1" "$prefix/share/man/man1/"
echo "install.sh: $prefix/bin/systemd-compose $ver, and its man page"
case ":$PATH:" in
*":$prefix/bin:"*) ;;
*) echo "install.sh: note: $prefix/bin is not on your PATH" >&2 ;;
esac
