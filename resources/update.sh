#!/bin/sh
# Update Portly to the latest release. The binary verifies and replaces itself.
set -eu
case $(uname -s) in
  MINGW* | MSYS* | CYGWIN*) directory=${PORTLY_INSTALL_DIR:-$(cygpath -u "$LOCALAPPDATA")/Programs/Portly} binary=portly.exe ;;
  *) directory=${PORTLY_INSTALL_DIR:-$HOME/.local/bin} binary=portly ;;
esac

if [ -x "$directory/$binary" ]; then
  portly="$directory/$binary"
elif command -v portly >/dev/null; then
  portly=$(command -v portly)
else
  echo "update: portly is not installed. Install it with:" >&2
  echo "  curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/install.sh | sh" >&2
  exit 1
fi
exec "$portly" update --yes
