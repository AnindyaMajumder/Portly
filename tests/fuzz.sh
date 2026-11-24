#!/bin/sh
# Every fuzz target in turn, with two workers.
set -eu
duration=${1:-10s}
for package in $(go list ./internal/...); do
  for target in $(go test -list '^Fuzz' "$package" | grep '^Fuzz' || true); do
    echo "$target"
    go test "$package" -run '^$' -fuzz "^$target\$" -fuzztime "$duration" -parallel 2 >/dev/null
  done
done
