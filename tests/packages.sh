#!/bin/sh
# Install and remove every amd64 Linux package with its own package manager, in throwaway containers.
set -eu
dist=$(realpath "${1:-dist}")
check='portly --version | grep -q "^portly " && test -f /usr/share/man/man1/portly.1 && test -f /usr/share/bash-completion/completions/portly'

# try installs one package in one image, checks it, removes it and checks it is gone.
try() {
  image=$1 pattern=$2 install=$3 remove=$4
  package=$(find "$dist" -maxdepth 1 -name "$pattern" | head -n 1)
  [ -n "$package" ] || { echo "no $pattern in $dist"; exit 1; }
  docker run --rm --pull=never -v "$dist:/dist:ro" "$image" sh -c \
    "$install /dist/$(basename "$package") >/dev/null && $check && $remove >/dev/null && ! command -v portly"
  echo "ok      $image $(basename "$package")"
}

try debian:stable-slim '*_amd64.deb' 'apt-get install -y -q' 'apt-get remove -y -q portly'
try fedora:latest '*.x86_64.rpm' "dnf install -y -q --disablerepo='*'" "dnf remove -y -q --disablerepo='*' portly"
try opensuse/leap:latest '*.x86_64.rpm' 'zypper -n --no-refresh install --allow-unsigned-rpm' 'zypper -n --no-refresh remove portly'
try alpine:latest '*_x86_64.apk' 'apk add --allow-untrusted' 'apk del portly'
try archlinux:latest '*-x86_64.pkg.tar.zst' 'pacman -U --noconfirm' 'pacman -R --noconfirm portly'
