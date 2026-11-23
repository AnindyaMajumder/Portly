# Portly Architecture

| | |
|---|---|
| Status | Proposed design for 1.0, under review. Nothing is implemented yet. |
| Language | Go 1.27.4, no cgo |
| Style | Modular monolith, one static binary |

Statements marked **[to verify]** come from documentation or source reading and have not been run on that platform yet. Section 16 lists the points that need a decision.

## 1. Purpose

Know what is running, where it is running from, and how to stop it, without memorizing PIDs or combining OS commands.

| Command | Does |
|---|---|
| `portly` | List accessible listening TCP ports and bound UDP ports |
| `portly --all` | Also show the likely-client UDP sockets hidden by default |
| `portly <port> ...` | Details for one or more ports. `8000/tcp` or `8000/udp` narrows |
| `portly stop <port> ...` | Show the exact targets, ask, stop gracefully |
| `portly stop <port>/udp` | Stop a UDP owner. A bare port always means TCP |
| `--yes` | Skip the prompt, never the checks |
| `portly stop <port> --force` | Stop gracefully first; if that fails, ask again and force kill |
| `--json` | Machine-readable output on every command above |
| `--help`, `--version`, `--verbose`, `completion` | Help, version, diagnostics on stderr, shell completion |

### Goals

1. **Safe:** refuse rather than guess. Never stop the wrong thing.
2. **Zero config:** no config file, daemon, database, account or mandatory Docker.
3. **Fast and small:** under 900 ms typical, 3 s in large environments, under 30 MB memory.
4. **Portable:** Linux, macOS, Windows on 32-bit, 64-bit and ARM where Go supports it.
5. **Honest:** partial results, with `Unknown` or `Permission denied` where data is missing.
6. **Scriptable:** stable, versioned JSON, exit codes and error codes.

### Not in 1.0

MCP server, choosing one of several owners by PID, port ranges, full-screen dashboard, remote hosts.

## 2. System context

```
 Developer ---------+
                    |
                    v
              +-----------+    sockets, processes     +------------------+
              |           | ------------------------> | Operating system |
              |           |                           +------------------+
              |           |    manifests              +------------------+
 Script or    |  Portly   | ------------------------> | Project files    |
 AI agent --> |           |                           +------------------+
  (--json)    |           |    optional, local only   +------------------+
              |           | - - - - - - - - - - - - > | Docker / Podman  |
              |           |                           +------------------+
              |           |    stop, or kill with     +------------------+
              |           | ------------------------> | Target process   |
              +-----------+    --force only           +------------------+
```

Portly makes no network calls outside the machine.

## 3. Structure

```
cmd/portly/            wires modules, sets the exit code
internal/
  cli/                 commands, table, detail, JSON, prompts
  app/                 use cases: List, Inspect, Stop
  domain/              types, identity rules, error codes
  modules/
    discovery/         sockets and processes, per OS
    project/           project root, name, evidence, confidence
    docker/            Docker and Podman client
    stopper/           stop policy (pure) and executor (per OS)
  platform/textsafe/   escape stripping, secret masking
tests/arch/            architecture rules as tests
tests/e2e/             real listeners, real stops
schema/                JSON schema
resources/             this document, user guide, diagrams
```

```
                         +-------+
                         |  cmd  |   wires everything
                         +-------+
                             |
                             v
                         +-------+        +----------+
                         |  cli  | -----> | textsafe |
                         +-------+        +----------+
                             |
                             v
                         +-------+
                         |  app  |   the only place modules meet
                         +-------+
                             |
        +-------------+------+------+-------------+
        v             v             v             v
  +-----------+ +-----------+ +-----------+ +-----------+
  | discovery | |  project  | |  docker   | |  stopper  |
  +-----------+ +-----------+ +-----------+ +-----------+
        |             |             |             |
        +-------------+------+------+-------------+
                             v
                        +--------+
                        | domain |   also used by app and cli
                        +--------+
```

| Layer | Owns | Must not |
|---|---|---|
| `domain` | Types, identity comparison, error codes | Do I/O, import other Portly code |
| `modules/*` | One capability and all its I/O | Import another module, print, exit |
| `app` | Combining modules, deadlines | Know about flags, colours or JSON |
| `cli` | Input, rendering, prompts | Decide what may be stopped |
| `cmd` | Wiring (builds modules, passes them to `app`) | Contain logic |

### Rules enforced by tests

- Dependencies point inward: `cli -> app -> modules -> domain`. Modules never import each other.
- Each third-party library is confined to the packages in section 4.
- Comments are at most 2 lines.
- The force-kill calls appear only in the `stopper` force executors and the `docker` kill call. Other processes' environment is never read.

### Code conventions

Intent-revealing names, early returns, one job per function (about 50 lines), no `utils` package, no global mutable state, errors wrapped once with context. OS code lives only in `_linux`, `_darwin`, `_windows` and `_unix` files. Interfaces are declared by their consumer.

## 4. Dependencies

| Library | Used for | Allowed in |
|---|---|---|
| Cobra | Commands, help, typo suggestions, completion | `cli` |
| Bubble Tea, Bubbles | Interactive stop prompt, spinner | `cli` |
| Lip Gloss | Tables and colour | `cli` |
| gopsutil | Process details on macOS and Windows | `discovery` |
| `golang.org/x/sys` | OS calls | `discovery`, `stopper`, `docker` |

- No Viper, Docker SDK or cgo.
- gopsutil is not used on Linux. Its start time depends on the wall clock and it honours a `HOST_PROC` override; both are unsafe for identity checks.
- gopsutil is not used for sockets anywhere. It hides owners that can't be read.
- Bubble Tea starts only for `portly stop` on a real terminal. `TERM=dumb` gets a plain `[y/N]`.

## 5. Core model

| Type | Meaning |
|---|---|
| `PortRef` | A port the user named, optionally with a protocol |
| `Socket` | Listening TCP or bound UDP socket: OS identity, address, port, creator, owner PIDs |
| `Process` | What the OS lets us see. Empty means unknown |
| `Container` | Running container, its published ports and Compose metadata |
| `Project` | Root, name, evidence, confidence |
| `Owner` | A process family or container behind a port |
| `PortReport` | Everything known about one port |
| `Snapshot` | One view of sockets and processes, plus whether it is complete |

**Process identity** is PID + start time + user + executable. If the start time is unknown, two processes never match.

## 6. Flows

### List and inspect

```
  cli             app                discovery      docker       project
   |               |                     |             |            |
   | List/Inspect  |                     |             |            |
   |-------------->|                     |             |            |
   |               |--- Scan ----------->|             |            |   in parallel,
   |               |--- Containers ------------------->|            |   docker max 400 ms
   |               |<-- sockets, processes             |            |
   |               |<-- containers (or none) ----------|            |
   |               |                                                |
   |               | attribute sockets to owners                    |
   |               |                                                |
   |               |--- Detect projects --------------------------->|   inside the
   |               |<-- project + evidence -------------------------|   3 s deadline
   |<--------------|                                                |
   | reports and warnings                                           |
   |                                                                |
   | sanitize, mask, render                                         |
```

A failing or slow source never fails the scan. Its fields become unknown and a warning is added.

### Stop

```
            +------------------------------+
            | Resolve sockets and owners   |
            +------------------------------+
                           |
                           v
                   +---------------+   refuse   +-------------------------+
                   |    Policy     | ---------> | Say why and what to do  |
                   +---------------+            +-------------------------+
                           | target                        ^
                           v                               |
            +------------------------------+               |
            | Show target and other ports  |               |
            +------------------------------+               |
                           |                               |
                           v                               |
                   +---------------+   no                  |
                   | Confirmed or  | ---> nothing is       |
                   |    --yes ?    |      stopped          |
                   +---------------+                       |
                           | yes                           |
                           v                               |
            +------------------------------+               |
            | Pin, resolve, policy again   |               |
            +------------------------------+               |
                           |                               |
                           v                               |
                   +---------------+   no                  |
                   | Same target ? | ----------------------+
                   +---------------+
                           | yes
                           v
            +------------------------------+
            | Send graceful stop, wait     |
            +------------------------------+
                           |
                           v
                   +---------------+   yes      +---------------------+
                   | Port free ?   | ---------> | Stopped             |
                   +---------------+            +---------------------+
                           | no
                           v
                   +---------------+   no       +---------------------+
                   |   --force ?   | ---------> | Report, suggest     |
                   +---------------+            | --force             |
                           | yes                +---------------------+
                           v
            +------------------------------+
            | Force kill steps (section 10)|
            +------------------------------+
```

## 7. Discovery

| | Linux | macOS | Windows |
|---|---|---|---|
| Sockets | `/proc/net/{tcp,udp}{,6}` | `/usr/sbin/lsof` | `GetExtendedTcpTable`, `GetExtendedUdpTable` |
| Owner | socket inode found in `/proc/*/fd` | reported by lsof | owner PID, checked against bind time |
| Process details | `/proc/PID/*` | sysctl, gopsutil | process handle, gopsutil |
| Start identity | kernel start ticks | start time in microseconds | creation time in 100 ns |
| Service manager | cgroup (systemd) | `launchctl list` | service manager, session 0 |
| Ephemeral range | `ip_local_port_range` | `net.inet.ip.portrange` | 49152-65535 |

Platform facts that shape the design:

- **Linux:** kernel addresses are hex in host byte order, so parsing must follow the CPU's endianness. The creator's user ID is visible even when the PID is not.
- **Linux as root:** the Docker daemon and docker-proxy can share one socket. On systemd socket activation, PID 1 shares the socket with the service. Both must lead to the correct answer or a refusal.
- **Linux inside a container:** processes sharing Portly's own container ID are native, not containers.
- **macOS:** lsof hides other users' sockets, so results are cross-checked with `netstat`. Sockets only `netstat` sees are shown as permission denied. If the cross-check fails, stop is refused. **[to verify]**
- **Windows:** the table keeps the PID that bound the socket even after it exits. An owner younger than its socket means the PID was reused, so the owner is unresolved. Parent PIDs get the same age check. **[to verify]**

External programs run by absolute path, with separate arguments, a minimal environment and a deadline.

### UDP

UDP has no listening state, so a browser looks like a server. Connected sockets are left out. Ports in the ephemeral range are hidden from the list, counted in the footer, and shown with `--all`. Lookup never hides unconnected sockets.

## 8. Project identification

| Evidence | Confidence |
|---|---|
| Working directory, up to the nearest manifest or `.git` | high |
| Path in startup arguments, or executable path, inside a project | medium |
| Ancestor's directory, same user, only if it is a package runner (npm, go, cargo, make, uv ...) | medium |
| None | unknown |

- Markers: `go.mod`, `package.json`, `pyproject.toml`, `Cargo.toml`, `pom.xml`, `.git` and similar. The manifest's name is used, then the directory name.
- `$HOME`, anything above it, and `/` are never projects. `node_modules`, `site-packages`, virtualenvs and toolchain caches are skipped.
- Shell, tmux and IDE directories don't count as evidence.
- Manifests are read only if they are regular files, up to 1 MiB, without blocking.
- Under sudo, the process owner's home is used, not root's.

## 9. Docker and Podman

- Plain HTTP over a local socket or named pipe. Podman serves the same API.
- Endpoint order: `DOCKER_HOST`, `DOCKER_CONTEXT`, the active context, then known paths (default, rootless, Docker Desktop, Colima, OrbStack, Podman).
- Remote engines are ignored, because their ports are not local.
- Sockets must be owned by root or the caller. Responses are size-limited.
- Limits: 400 ms for listing, 3 s for stop.
- Only host-published ports are listed. Exposed-only ports are not.
- Compose labels give the project, service and directory. Host-network containers are matched through the process's cgroup.
- With Docker's userland proxy disabled there may be no host socket, so published ports are listed directly.

## 10. Stop safety

### Invariants

1. Never force kill unless the user passed `--force`, and never before a graceful stop was tried and failed. The only exception is a target with no graceful method.
2. Never signal a PID that was not pinned and revalidated.
3. Ambiguous, unresolved, protected, supervised or forwarder means refuse.
4. `--yes` skips only prompts. It never implies `--force`, and `--force` never skips a check.
5. A bare port means TCP.
6. Never run sudo. Print the command.
7. Success only after the socket is gone and the port is free.

### Policy

| Situation | Result |
|---|---|
| One resolved owner | Allowed |
| Family: one owner is the ancestor of all others, same user, and handles SIGTERM | Allowed, only the root is signalled |
| Plain reloader: parent has the same executable, arguments, directory and user, and no socket | Allowed, the listener is signalled |
| One unambiguous container | Allowed, through the engine API |
| Unrelated owners, or native plus container | Refused: ambiguous |
| Any listener on the port without a known owner | Refused: unresolved, or permission denied |
| Socket's user differs from the visible owner's user (non-root) | Refused: hidden owners possible |
| PID 1, kernel, Portly and its ancestors, system processes | Refused: protected |
| docker-proxy, rootlessport, Docker Desktop, Lima, Colima, gvproxy, OrbStack, wslrelay | Refused: forwarder |
| nodemon, pm2, supervisord, air, watchexec, `node --watch`, systemd, launchd, Windows services | Refused: supervised, with the right command |
| Swarm or Kubernetes container | Refused |
| Container whose stop signal is a force kill | Refused without `--force` |

The policy is a pure function: snapshot in, decision out. It reads no clock and no OS state, so every rule can be tested directly.

### Several ports

Each port is an independent operation with every check. Duplicates and shared targets are signalled once. Stops run one at a time, each revalidated right before its own signal. A refused port does not block the others. Portly never stops a process whose port was not named.

### Executors

| | Linux | macOS | Windows | Container |
|---|---|---|---|---|
| Pin | pidfd | not available | process handle | ID and start time |
| Stop | SIGTERM, then SIGCONT so a suspended process receives it | same | Ctrl+C through a detached helper | `POST /stop?t=-1` |
| Remaining race | none with pidfd, tiny on old kernels | tiny | handle prevents PID reuse | tiny |

- **Windows:** the helper attaches to the target's console and continues only if everything on it is the target, its descendants, or ancestors that are known shells or package runners. Ctrl+C reaches all of them, as if pressed in that window. Otherwise Portly refuses. **[to verify]**
- **Containers:** the engine's default stop ends in a force-kill, so Portly passes `t=-1`. A container that ignores the signal keeps running and the request stays pending at the engine. Portly says so and suggests `--force`.

### Force kill

A last resort for a process that ignores a graceful stop. It can lose unsaved data.

```
  graceful stop tried, port still held
                 |
                 v
  +--------------------------------+
  | Show the target again and warn |
  +--------------------------------+
                 |
                 v
  +--------------------------------+   no
  | Second confirmation or --yes ? | ------> left running, exit 7
  +--------------------------------+
                 | yes
                 v
  +--------------------------------+   changed
  | Pin, resolve, policy again     | ------> refused, exit 5
  +--------------------------------+
                 | same target
                 v
  +--------------------------------+
  | Kill, then verify port is free |
  +--------------------------------+
```

| | Linux | macOS | Windows | Container |
|---|---|---|---|---|
| Force kill | SIGKILL through the pidfd | SIGKILL | `TerminateProcess` on the pinned handle | engine API `kill` |

- `--force` alone asks twice. `--force --yes` asks nothing. `--yes` alone never kills.
- Every refusal in the policy table still applies, apart from the container stop signal row. Ambiguous, protected, supervised, forwarder and unresolved targets can't be forced.
- If there is no graceful method (a Windows target without a safe console, a container whose stop signal is a force kill), `--force` goes straight to the confirmation. Nothing graceful was possible, and Portly says so.
- **Families:** killing only the root would leave workers holding the port. Portly shows and pins every member that holds the socket, kills the root first, then the members still holding it. Processes that don't hold the socket are never killed.
- **macOS:** there is no pin, so identity is checked immediately before the kill and a tiny race remains.
- JSON reports `"method": "graceful"` or `"method": "forced"`, plus the PIDs that were killed.
- Without a terminal, `--force` needs `--yes`. Otherwise the result is `confirmation_required`.

### Verification

Poll for 5 s (containers 10 s). Outcomes: stopped; port free but process still draining; still held; restarted; taken over by another process.

## 11. Security

Portly may run as root. Assume a hostile local user controls process names, arguments, directories, manifests, container labels, environment and process lifetimes.

| Threat | Defence |
|---|---|
| PID reuse or exec between check and signal | Pin, resolve again, compare identity, signal through the pin |
| Wrong target through shared or hidden sockets | Refuse unless every listener resolves to one family |
| Force kill used by accident or by an agent | Explicit `--force`, graceful first, second confirmation, all checks rerun |
| Planted binaries on PATH | Absolute paths, no shell, minimal environment |
| Manifest that is a FIFO, device or huge file | Non-blocking open, regular files only, size limit, deadline |
| Terminal escape or bidi sequences in names | All process-derived text is sanitized |
| Secrets in command lines | Masked in terminal and JSON, best effort. Environments are never read |
| Planted engine socket or hostile `DOCKER_HOST` | Local only, owner check, bounded responses |
| Slow disks, huge process tables | Deadlines, partial results |

Failure always means refuse. Unknown is never treated as safe.

**Limits:** secret masking is pattern-based and can miss unusual formats. Root and the OS are trusted.

## 12. Output contract

- stdout is data, stderr is diagnostics.
- `--json` prints exactly one document, errors included, without colour or prompts. Inaccessible fields are `null`.
- `schema_version` is `"1"`. Within 1.x fields may be added, never changed or removed.
- `stop --json` without `--yes` returns `confirmation_required` and the target, which works as a dry run.
- Stop output is a `results` array with one entry per port.
- Colour only on a terminal, and `NO_COLOR` is respected.

| Exit | Meaning |
|---|---|
| 0 | OK, including a lookup of a free port |
| 1 | Internal error |
| 2 | Bad usage |
| 3 | Stop: nothing is listening |
| 4 | Permission denied or owner unresolved |
| 5 | Refused: ambiguous, protected, forwarder, supervised, changed or unsupported |
| 6 | Confirmation required or cancelled |
| 7 | Signal sent or force kill declined, but the port is still used, restarted or taken over |
| 8 | Several ports with different outcomes |

## 13. Platforms

| OS | Architectures |
|---|---|
| Linux | 386, amd64, armv6+, arm64, riscv64, ppc64le, s390x |
| macOS 13+ | amd64, arm64 |
| Windows | 386, amd64, arm64 |

Go no longer supports 32-bit macOS or 32-bit ARM Windows. Binaries are static, so they don't depend on the Linux distribution's C library.

## 14. Quality

| Layer | Proves |
|---|---|
| Architecture tests | Import direction, library boundaries, comment limit |
| Unit and golden | Parsers, detection, rendering, JSON against the schema |
| Policy | One test per rule in section 10, plus random snapshots that must never select a protected process |
| Fuzz | Every parser of untrusted data, masking, arguments |
| End to end | Graceful stop, ignored signal, force kill (never without `--force`, refusals still hold), respawn, suspended process, dual stack, two owners, UDP, several ports |
| Containers | Throwaway Docker and Podman containers |
| Distributions | Alpine, Debian, Fedora, Arch, openSUSE |
| Budgets | Time, memory, 5,000-process scan |
| Static analysis | gofmt, vet for each OS, staticcheck, govulncheck, race detector |

Coverage target: 85% overall, every branch of the stop policy.

## 15. Build and release

- **CI:** on push to `main` only. Linux, macOS and Windows on x64 and ARM, plus 32-bit builds and distribution containers. Least-privilege token, actions pinned by SHA.
- **Release:** on a `v*` tag only. Archives, deb, rpm, apk and Arch packages, man page, completions, signed checksums, SBOM and build attestation on GitHub Releases.
- Binaries are never committed. `resources/releases/` is a local folder.
- SemVer. Section 12 is the compatibility contract.

## 16. Points to audit

| # | Question | Proposed |
|---|---|---|
| 1 | Process families on macOS: whether a process handles SIGTERM can't be read reliably | Refuse gracefully and with `--force`, so gunicorn and `uvicorn --reload` can't be stopped there |
| 2 | Windows graceful stop refuses when the console is shared with an IDE or agent | Refuse, `--force` is the way out |
| 3 | Should UDP stop exist at all? | Yes, only with `/udp` |
| 4 | Ambiguous owners have no way forward inside Portly | Refuse. `--pid` could come later |
| 5 | macOS relies on the text output of `lsof` and `netstat` | Accept. Native calls need far more code |
| 6 | Unresolved owner shares exit 4 with permission denied | Accept. Error codes differ |
| 7 | CI only on `main` means macOS and Windows first run after code lands there | Accept for now |
| 8 | Four UI libraries against the 30 MB and 900 ms budgets | Measure first, decide with numbers |
| 9 | Names in the protected, forwarder and supervisor lists will age | Accept. Wrongly listed names only cause refusals; missing ones are a risk |
| 10 | App-launched systemd user units (`app-*.service`) | Managed only if systemd is the direct parent |
| 11 | Should `--force --yes` work unattended, for example from an AI agent? | Yes. Both flags are explicit, and every check still applies |
| 12 | Should `--force` kill a whole owner family? | Yes, but only members holding the socket |
| 13 | Should supervised processes stay refused with `--force`? | Yes, the supervisor would restart them |
| 14 | FR-03 says never escalate automatically to SIGKILL | Kept. Escalation happens only after `--force` |
