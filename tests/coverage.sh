#!/bin/sh
# At least 85% overall, and every statement of the stop policy.
set -eu
profile=$(mktemp)
trap 'rm -f "$profile"' EXIT
go test -count=1 -coverpkg=./internal/... -coverprofile="$profile" ./internal/... >/dev/null
total=$(go tool cover -func="$profile" | awk '/^total:/ {sub("%","",$3); print $3}')
echo "total $total%"
awk -v total="$total" 'BEGIN { exit !(total >= 85) }' || { echo "below 85%"; exit 1; }
policy='stopper/(policy|native|container|evidence|names|planid)\.go'
missed=$(go tool cover -func="$profile" | grep -E "$policy" | grep -v '100.0%' || true)
[ -z "$missed" ] || { echo "stop policy is not fully covered:"; echo "$missed"; exit 1; }
echo "stop policy 100%"
