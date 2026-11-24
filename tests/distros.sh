#!/bin/sh
# Run the static binary in images that are already local. Never pulls.
set -eu
binary=$(realpath "${1:-bin/portly}")
ran=0
for image in redis:7-alpine alpine:latest debian:stable-slim fedora:latest archlinux:latest opensuse/leap:latest; do
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "skipped $image: not available locally"
    continue
  fi
  docker run --rm --pull=never --network=none --memory=64m --pids-limit=64 --read-only \
    -v "$binary:/portly:ro" --entrypoint /portly "$image" --json | grep -q '"schema_version": "1"'
  echo "ok      $image"
  ran=$((ran + 1))
done
[ "$ran" -gt 0 ] || { echo "no image available"; exit 1; }
