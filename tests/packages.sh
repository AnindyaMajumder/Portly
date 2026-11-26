#!/bin/sh
# Install and remove every amd64 Linux package with its own package manager, in throwaway containers.
set -eu
dist=$(realpath "${1:-dist}")

# try installs one package, checks the binary runs and the package ships its extras, removes it.
# Container images skip man pages on disk, so the package's own file list is checked instead.
try() {
  image=$1 pattern=$2 install=$3 list=$4 remove=$5
  package=$(find "$dist" -maxdepth 1 -name "$pattern" | head -n 1)
  [ -n "$package" ] || { echo "no $pattern in $dist"; exit 1; }
  script="set -e
    $install /dist/$(basename "$package") >/dev/null
    portly --version | grep -q '^portly '
    $list portly 2>/dev/null | grep -q 'share/man/man1/portly.1'
    $list portly 2>/dev/null | grep -q 'share/bash-completion/completions/portly'
    $remove portly >/dev/null
    ! test -e /usr/bin/portly"
  docker run --rm --pull=never -e DEBIAN_FRONTEND=noninteractive -v "$dist:/dist:ro" "$image" sh -c "$script" ||
    { echo "failed  $image $(basename "$package")"; exit 1; }
  echo "ok      $image $(basename "$package")"
}

try debian:stable-slim '*_linux_amd64.deb' 'apt-get install -y -q' 'dpkg -L' 'apt-get remove -y -q'
try fedora:latest '*_linux_amd64.rpm' "dnf install -y -q --disablerepo='*'" 'rpm -ql' "dnf remove -y -q --disablerepo='*'"
try opensuse/leap:latest '*_linux_amd64.rpm' 'zypper -n --no-refresh install --allow-unsigned-rpm' 'rpm -ql' 'zypper -n --no-refresh remove'
try alpine:latest '*_linux_amd64.apk' 'apk add --allow-untrusted' 'apk info -L' 'apk del'
try archlinux:latest '*_linux_amd64.pkg.tar.zst' 'pacman -U --noconfirm' 'pacman -Ql' 'pacman -R --noconfirm'
