#!/bin/sh
# Install Portly for the current user. Verifies the checksum, never uses sudo.
set -eu
repository=https://github.com/AnindyaMajumder/Portly
directory=${PORTLY_INSTALL_DIR:-$HOME/.local/bin}

fail() { echo "install: $1" >&2; exit 1; }
command -v curl >/dev/null || fail "curl is required"
command -v tar >/dev/null || fail "tar is required"

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported system $(uname -s)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  i386 | i686) arch=386 ;;
  armv6*) arch=armv6 ;;
  armv7*) arch=armv7 ;;
  riscv64 | ppc64le | s390x) arch=$(uname -m) ;;
  *) fail "unsupported processor $(uname -m)" ;;
esac

version=${PORTLY_VERSION:-}
if [ -z "$version" ]; then
  version=$(curl -fsSLI --proto '=https' -o /dev/null -w '%{url_effective}' "$repository/releases/latest")
  version=${version##*/v}
fi
case $version in
  "" | *[!0-9A-Za-z.-]*) fail "could not find the latest version" ;;
esac

temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
archive="portly_${version}_${os}_${arch}.tar.gz"
for file in "$archive" checksums.txt; do
  curl -fsSL --proto '=https' -o "$temporary/$file" "$repository/releases/download/v$version/$file" || fail "could not download $file"
done

expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$temporary/checksums.txt")
if command -v sha256sum >/dev/null; then
  actual=$(sha256sum "$temporary/$archive" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$temporary/$archive" | cut -d' ' -f1)
fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "checksum does not match, nothing was installed"

tar -xzf "$temporary/$archive" -C "$temporary" portly
mkdir -p "$directory"
mv "$temporary/portly" "$directory/portly"
echo "Installed portly $version in $directory"
case ":$PATH:" in
  *":$directory:"*) ;;
  *) echo "Add $directory to your PATH" ;;
esac
