#!/bin/sh
# Remove Portly installed by install.sh. Never uses sudo.
set -eu
fail() { echo "uninstall: $1" >&2; exit 1; }
case $(uname -s) in
  MINGW* | MSYS* | CYGWIN*) directory=${PORTLY_INSTALL_DIR:-$(cygpath -u "$LOCALAPPDATA")/Programs/Portly} binary=portly.exe ;;
  *) directory=${PORTLY_INSTALL_DIR:-$HOME/.local/bin} binary=portly ;;
esac

# The remove command of the package manager on this system
package_remove() {
  for manager in "apt-get:sudo apt remove portly" "dnf:sudo dnf remove portly" "zypper:sudo zypper remove portly" \
    "apk:sudo apk del portly" "pacman:sudo pacman -R portly"; do
    command -v "${manager%%:*}" >/dev/null && { echo "${manager#*:}"; return; }
  done
  echo "your package manager's remove command"
}

if [ ! -f "$directory/$binary" ]; then
  found=$(command -v portly 2>/dev/null || true)
  case $found in
    "") fail "portly is not installed in $directory" ;;
    /usr/bin/* | /usr/sbin/* | /bin/* | /sbin/*) fail "a package manager installed $found. Remove it with: $(package_remove)" ;;
    *) fail "portly is in $(dirname "$found"), not $directory. To remove it from there:
  curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/uninstall.sh | PORTLY_INSTALL_DIR='$(dirname "$found")' sh" ;;
  esac
fi
"$directory/$binary" --version 2>/dev/null | grep -q '^portly ' || fail "$directory/$binary is not portly, so it was left alone"
rm -f "$directory/$binary" "$directory/$binary".old* 2>/dev/null ||
  fail "can't remove $directory/$binary. To remove it as administrator: sudo rm '$directory/$binary'"
# Only the Windows folder belongs to Portly alone
[ "$binary" = portly.exe ] && rmdir "$directory" 2>/dev/null || true
echo "Removed portly from $directory"
