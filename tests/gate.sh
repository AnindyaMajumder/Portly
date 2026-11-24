#!/bin/bash
# Evidence gate environments that change the machine. Only for disposable CI runners.
set -euo pipefail
if [ "${GITHUB_ACTIONS:-}" != true ] || [ "${RUNNER_ENVIRONMENT:-}" != github-hosted ]; then
  echo "refused: this changes system settings and runs only on GitHub's disposable runners"
  exit 1
fi
cd "$(dirname "$0")/.."
export GOFLAGS=-buildvcs=false

# sudo-rs keeps no environment, so it is passed on explicitly
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}" GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
keep=()
collect() {
  local name
  for name in PATH HOME GOFLAGS GOCACHE GOMODCACHE GOTOOLCHAIN XDG_RUNTIME_DIR DBUS_SESSION_BUS_ADDRESS \
    PORTLY_DOCKER_TESTS PORTLY_PODMAN_TESTS PORTLY_ROUTED_NETWORK; do
    [ -z "${!name:-}" ] || keep+=("$name=${!name}")
  done
}

# The runner is a system service, so tests move to their own scope
scope() {
  sudo env "${keep[@]}" systemd-run --scope --quiet setpriv --reuid="$(id -u)" --regid="$(id -g)" --init-groups "$@"
}

run() {
  local output status=0
  output=$(mktemp)
  "$@" 2>&1 | tee "$output" || status=$?
  # A skipped environment is not evidence
  if grep -q -- '--- SKIP' "$output" || ! grep -q -- '--- PASS' "$output"; then
    echo "nothing was proven"
    status=1
  fi
  return "$status"
}

tests() { echo go test -count=1 -timeout 12m -v -run "$1" ./tests/e2e; }

case ${1:-} in
  hidden-proc)
    trap 'sudo mount -o remount,hidepid=off /proc' EXIT
    sudo mount -o remount,hidepid=invisible /proc
    collect
    run scope $(tests '^TestHiddenProcesses$')
    ;;
  root)
    export PORTLY_DOCKER_TESTS=1
    collect
    run sudo env "${keep[@]}" GOCACHE=/tmp/root-go-cache systemd-run --scope --quiet \
      $(tests '^TestDocker(HostNetwork|GracefulStop|IgnoredStopIsPending|Force)$')
    ;;
  routed)
    export PORTLY_DOCKER_TESTS=1 PORTLY_ROUTED_NETWORK=portly-routed
    docker network create -o com.docker.network.bridge.gateway_mode_ipv4=routed portly-routed
    collect
    run scope $(tests '^TestDockerRouted$')
    ;;
  no-proxy)
    export PORTLY_DOCKER_TESTS=1
    sudo sh -c 'f=/etc/docker/daemon.json; [ -s $f ] || echo "{}" > $f; jq ".\"userland-proxy\"=false" $f > $f.new && mv $f.new $f && systemctl restart docker'
    collect
    run scope $(tests '^TestDocker(Lookup|ReleaseRefuses|Guard|GracefulStop|IgnoredStopIsPending|Force|KillStopSignal)$')
    ;;
  podman)
    export PORTLY_PODMAN_TESTS=1 XDG_RUNTIME_DIR="/run/user/$(id -u)"
    export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
    sudo loginctl enable-linger "$(id -un)"
    timeout 30 sh -c "until [ -S '$XDG_RUNTIME_DIR/bus' ]; do sleep 1; done"
    collect
    podman version --format '{{.Client.Version}}'
    podman pull -q docker.io/library/redis:7-alpine
    mkdir -p "$XDG_RUNTIME_DIR/podman"
    scope podman system service --time=0 "unix://$XDG_RUNTIME_DIR/podman/podman.sock" &
    timeout 30 sh -c "until [ -S '$XDG_RUNTIME_DIR/podman/podman.sock' ]; do sleep 1; done"
    run scope $(tests '^TestPodman')
    ;;
  *)
    echo "usage: $0 hidden-proc|root|routed|no-proxy|podman"
    exit 2
    ;;
esac
