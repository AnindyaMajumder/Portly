# Contributing

## Start

You need Go 1.27.4 or later, `make`, and a C compiler such as gcc for the race detector in `make check`. Docker is only needed for the container tests.

```
git clone https://github.com/AnindyaMajumder/Portly.git
cd Portly
make check        # format, vet for every OS, architecture rules, unit tests with -race, build
bin/portly        # try it
```

`make help` lists every target. Run one test with `go test ./internal/modules/stopper -run TestRefusals/shared_by_strangers -v`.

## Where code goes

```
cmd/portly/          main: wires the modules, sets the exit status
internal/cli/        flags, tables, JSON, prompts (Cobra and Lip Gloss live only here)
internal/app/        use cases: List, Inspect, Plan and Execute a stop, Update
internal/modules/    one capability each, never importing each other
  discovery/         sockets and processes, per OS
  project/           which project a process belongs to
  docker/            Docker and Podman through their local API
  stopper/           stop policy (pure) and executors (per OS)
  updater/           downloads, verifies and installs releases
internal/domain/     types, reason codes and pure rules, imports nothing from Portly
internal/platform/textsafe/   terminal-safe text, secret masking
```

Imports point down only: `cli -> app -> modules -> domain`. `tests/arch` fails the build otherwise. The design and its reasons are in [resources/ARCHITECTURE.md](resources/ARCHITECTURE.md).

## Rules

1. Every function starts with a 1-2 line comment. No comment is longer.
2. OS-specific code goes in `_linux.go`, `_darwin.go`, `_windows.go` or `_unix.go` files.
3. Every package ships unit tests. Every safety rule needs a test that allows, one that refuses, and one where the state changes in between.
4. No new dependency, configuration file, daemon or cgo without discussing it first.
5. One logical change per commit, with a subject like `feat: add port ranges`.

Tests may only stop processes and containers they started. The `portly_evidence` build tag opens the evidence gates for tests, and refuses everything that is not a child of the test binary or a labelled test container. It must never be released.

## Tests

| Target | What it runs | Needs |
|---|---|---|
| `make check` | format, vet, architecture, unit tests with `-race`, build | Go |
| `make cover` | 85% overall, 100% of the stop policy | Go |
| `make fuzz` | every parser for `FUZZTIME` | Go |
| `make e2e` | real listeners, real stops of servers the tests start | Linux |
| `make gate` | evidence gates with real gunicorn, uvicorn, Django, Flask | Linux, Python packages from `tests/e2e/gate-requirements.txt` |
| `make docker-test` | throwaway labelled containers | Docker |
| `make cycles` | 1,000 stops in a throwaway container | Docker, about 30 minutes |
| `make distro-test` | the static binary in Alpine, Debian, Fedora, Arch, openSUSE | Docker images already pulled |
| `make packages` | install and remove each Linux package | Docker, packages from `make snapshot` |
| `make check-all` | all of the above except `cycles` and `packages` | all of the above |

## Recipes

**Change output.** Edit `internal/cli`, then `go test ./internal/cli -update` and review the golden files that changed.

**Add a reason code.** Add it to `schema/portly.v1.schema.json` first, then `internal/domain/reason.go` with its exit status, `resources/json.md`, and an explanation in `internal/cli/messages.go`. The contract tests fail until all four agree. Codes and exit statuses never change within 1.x.

**Add OS-specific code.** Give every OS the same function in its suffixed file, and run `make vet` to compile all three.

## Releases

A maintainer pushes a `v*` tag. `.github/workflows/release.yml` builds every target with GoReleaser, signs the checksums, publishes the release, then runs the install, update and uninstall commands on Linux, macOS and Windows. `make snapshot` builds the same packages locally without publishing.
