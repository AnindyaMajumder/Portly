#!/bin/sh
# Install Portly for the current user. Verifies the checksum, never uses sudo.
set -eu
repository=https://github.com/AnindyaMajumder/Portly

fail() { echo "install: $1" >&2; exit 1; }
command -v curl >/dev/null || fail "curl is required"

# Git Bash, MSYS2 and Cygwin on Windows get the zip and the same folder as install.ps1
case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) os=windows ;;
  *) fail "unsupported system $(uname -s)" ;;
esac
if [ "$os" = windows ]; then
  directory=${PORTLY_INSTALL_DIR:-$(cygpath -u "$LOCALAPPDATA")/Programs/Portly}
  binary=portly.exe extension=zip
  command -v unzip >/dev/null || fail "unzip is required"
else
  directory=${PORTLY_INSTALL_DIR:-$HOME/.local/bin}
  binary=portly extension=tar.gz
  command -v tar >/dev/null || fail "tar is required"
fi
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
version=${version#v}
if [ -z "$version" ]; then
  version=$(curl -fsSLI --proto '=https' -o /dev/null -w '%{url_effective}' "$repository/releases/latest")
  version=${version##*/v}
fi
case $version in
  "" | *[!0-9A-Za-z.-]*) fail "could not find the latest release, see $repository/releases" ;;
esac

temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
archive="portly_${version}_${os}_${arch}.${extension}"
for file in "$archive" checksums.txt; do
  curl -fsSL --proto '=https' -o "$temporary/$file" "$repository/releases/download/v$version/$file" ||
    fail "could not download $file. Check PORTLY_VERSION and your connection, or see $repository/releases"
done

expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$temporary/checksums.txt")
if command -v sha256sum >/dev/null; then
  actual=$(sha256sum "$temporary/$archive" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$temporary/$archive" | cut -d' ' -f1)
fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "checksum does not match, nothing was installed"

if [ "$extension" = zip ]; then
  unzip -q -o "$temporary/$archive" "$binary" -d "$temporary"
else
  tar -xzf "$temporary/$archive" -C "$temporary" "$binary"
fi
mkdir -p "$directory"
mv "$temporary/$binary" "$directory/$binary"
echo "Installed portly $version in $directory"
case ":$PATH:" in
  *":$directory:"*) ;;
  *)
    case ${SHELL:-} in
      */zsh) profile="$HOME/.zshrc" ;;
      */bash) profile="$HOME/.bashrc" ;;
      *) profile="$HOME/.profile" ;;
    esac
    echo "$directory is not on your PATH. Add it, then open a new terminal:"
    echo "  echo 'export PATH=\"$directory:\$PATH\"' >> $profile"
    ;;
esac
