# Portly Architecture

| | |
|---|---|
| Status | Implemented. Every evidence gate is still closed, so stopping is refused on every platform. |
| Language | Go 1.27.4, no cgo |
| Style | Modular monolith, one static binary |

Statements marked **[to verify]** come from documentation or source reading and have not been run on that platform yet. That covers all macOS and Windows behaviour. Section 16 lists the tests each platform must pass before stopping is enabled there.

## 1. Purpose

Know what is running, where it is running from, and how to stop it, without memorizing PIDs or combining OS commands.

**Principle:** maximize discovery, minimize assumptions, and act only when ownership and impact are verified.

| Command | Does |
|---|---|
| `portly` | List accessible listening TCP ports and bound UDP endpoints |
| `portly --all` | Also show the likely-client UDP sockets hidden by default |
| `portly <port> ...` | Details for one or more ports. `8000/tcp` or `8000/udp` narrows |
| `portly stop <port> ...` | Show the exact targets, ask, stop gracefully |
| `portly stop <port>/udp` | Stop a UDP owner. A bare port always means TCP |
| `--yes` | Skip the prompt, never the checks |
| `portly stop <port> --force` | Stop gracefully first; if that fails, ask again and force kill |
| `portly stop <port> --expect <plan id>` | Stop only if the targets are exactly the ones inspected earlier |
| `--json` | Machine-readable output on every command above |
| `--help`, `--version`, `--verbose`, `completion` | Help, version, diagnostics on stderr, shell completion |

### Goals

1. **Safe:** refuse when ownership or impact is not verified. Section 10 lists the exact guarantees and the races that remain.
2. **Zero config:** no config file, daemon, database, account or mandatory Docker.
3. **Fast and small:** list and inspect under 900 ms typical, 3 s in large environments, under 30 MB memory. Stopping takes as long as the target needs, within the limits in section 10.
4. **Portable:** Linux, macOS and Windows, with the support tiers in section 13. Guarantees are stated per platform, not assumed equal.
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
    stopper/           stop policy (pure) and executors (per OS)
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
| `domain` | Types, identity comparison, stop plan, error codes | Do I/O, import other Portly code |
| `modules/*` | One capability and all its I/O | Import another module, print, exit |
| `app` | Combining modules, deadlines | Know about flags, colours or JSON |
| `cli` | Input, rendering, prompts | Decide what may be stopped |
| `cmd` | Wiring (builds modules, passes them to `app`) | Contain logic |

### Rules enforced by tests

- Dependencies point inward: `cli -> app -> modules -> domain`. Modules never import each other.
- Each third-party library is confined to the packages in section 4.
- Discovery describes, policy decides, executors act. Executors accept only an approved and revalidated stop target, and signal calls exist nowhere else.
- Comments are at most 2 lines.
- The force-kill calls appear only in the `stopper` force executors and the `docker` kill call. Other processes' environment is never read.

### Code conventions

Intent-revealing names, early returns, one job per function (about 50 lines), no `utils` package, no global mutable state, errors wrapped once with context. OS code lives only in `_linux`, `_darwin`, `_windows` and `_unix` files. Interfaces are declared by their consumer.

## 4. Dependencies

| Library | Used for | Allowed in |
|---|---|---|
| Cobra | Commands, help, typo suggestions, completion | `cli` |
| Lip Gloss | Tables and colour | `cli` |
| gopsutil | Process details on macOS and Windows | `discovery` |
| `golang.org/x/sys` | OS calls | `discovery`, `stopper`, `docker` |

- No Viper, Docker SDK or cgo.
- gopsutil is not used on Linux. Its start time depends on the wall clock and it honours a `HOST_PROC` override; both are unsafe for identity checks.
- gopsutil is not used for sockets anywhere. It hides owners that can't be read.
- The stop prompt is a plain `[y/N]` from the standard library, and the force prompt asks for a word. There is no terminal UI framework.

## 5. Core model

| Type | Meaning |
|---|---|
| `PortRef` | A port the user named, optionally with a protocol |
| `Socket` | Listening TCP socket or bound UDP endpoint, with its identity and ownership evidence |
| `Process` | What the OS lets us see. Empty means unknown |
| `Container` | Running container, its published ports and Compose metadata |
| `Project` | Root, name, evidence, confidence |
| `Owner` | A process family or container behind a port |
| `PortReport` | Everything known about one port |
| `Snapshot` | One view of sockets and processes, and how complete it is |
| `StopPlan` | All targets and refusals for one stop command |
| `StopTarget` | One process, family or container to stop: expected identity, evidence, named ports, other affected endpoints and child processes |

Three things are kept apart. A port number alone identifies none of them.

| Identity | Made of | Answers |
|---|---|---|
| Process | PID, start time, user, executable | Which process is this? |
| Socket | Protocol, family, local address and scope, port, network namespace, OS socket id | Which socket was observed? |
| Ownership evidence | Source and completeness, per endpoint | Why do we believe this owner holds this socket? |

- The OS socket id is the inode on Linux, the kernel socket address on macOS **[to verify]**, and the owner PID with bind time on Windows.
- If a start time is unknown, two processes never match. If a socket id is unknown, two sockets never match.
- Completeness is `complete`, `partial` or `unavailable`, recorded per endpoint and source, never for a whole scan.

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
            | for every named port         |
            +------------------------------+
                           |
                           v
            +------------------------------+
            | Group by target              |
            +------------------------------+
                           |
                           v
                   +---------------+   refuse   +-------------------------+
                   |    Policy     | ---------> | Say why and what to do  |
                   +---------------+            +-------------------------+
                           | stop plan                     ^
                           v                               |
                   +---------------+   no                  |
                   | --expect      | ----------------------+
                   | matches ?     |                       |
                   +---------------+                       |
                           | yes, or not given             |
                           v                               |
            +------------------------------+               |
            | Show targets, other ports    |               |
            | and child processes          |               |
            +------------------------------+               |
                           |                               |
                           v                               |
                   +---------------+   no                  |
                   | Confirmed or  | ---> nothing is       |
                   |    --yes ?    |      stopped          |
                   +---------------+                       |
                           | yes, then per target          |
                           v                               |
            +------------------------------+               |
            | Pin, new snapshot, policy    |               |
            | again, compare with plan     |               |
            +------------------------------+               |
                           |                               |
                           v                               |
                   +---------------+   no                  |
                   | Same process  | ----------------------+
                   | and socket,   |
                   | pin alive ?   |
                   +---------------+
                           | yes
                           v
            +------------------------------+
            | Send graceful stop, wait     |
            +------------------------------+
                           |
                           v
                   +---------------+   yes      +---------------------+
                   | Target gone,  | ---------> | Stopped             |
                   | port free,    |            +---------------------+
                   | also 1 s on ? |
                   +---------------+
                           | no
                           v
                   +---------------+   no       +---------------------+
                   | Delivered, the| ---------> | Report the outcome  |
                   | target still  |            +---------------------+
                   | holds it, and |
                   | --force ?     |
                   +---------------+
                           | yes
                           v
            +------------------------------+
            | Force kill steps (section 10)|
            +------------------------------+
```

## 7. Discovery

| | Linux | macOS | Windows |
|---|---|---|---|
| Sockets | `/proc/net/{tcp,udp}{,6}` | `/usr/sbin/lsof` | `GetExtendedTcpTable`, `GetExtendedUdpTable` |
| Owner | socket inode found in `/proc/*/fd` | reported by lsof | owner PID, checked against bind time (`TCP_TABLE_OWNER_MODULE_LISTENER`, `UDP_TABLE_OWNER_MODULE`) |
| Process details | `/proc/PID/*` | sysctl, gopsutil, `/bin/ps` for arguments | process handle, gopsutil |
| Start identity | kernel start ticks | start time in microseconds | creation time in 100 ns |
| Service manager | cgroup (systemd) | launchd system domain and the owner's domains | service manager, session 0 |
| Ephemeral range | `ip_local_port_range` | `net.inet.ip.portrange` | 49152-65535 |

Platform facts that shape the design:

- **Scope:** Portly sees the sockets of its own network namespace, not of the whole machine. Container namespaces are covered through the engine.
- **Linux:** kernel addresses are hex in host byte order, so parsing must follow the CPU's endianness. The creator's user ID is visible even when the PID is not.
- **Linux as root:** the Docker daemon and docker-proxy can share one socket. On systemd socket activation, PID 1 shares the socket with the service. Both must lead to the correct answer or a refusal.
- **Linux inside a container:** processes sharing Portly's own container ID are native, not containers.
- **macOS:** lsof hides other users' sockets, so results are cross-checked with `netstat`. Sockets only `netstat` sees are shown as permission denied. The cross-check supports discovery and is not proof that every holder was found. If it fails, stop is refused. macOS has full discovery and limited stopping. **[to verify]**
- **Windows:** the table keeps the PID that bound the socket even after it exits. Only the `OWNER_MODULE` tables carry the bind time. An owner younger than its socket means the PID was reused, so the owner is unresolved. A missing or zero bind time also means unresolved. Parent PIDs get the same age check. **[to verify]**

External programs run by absolute path, with separate arguments, a minimal environment and a deadline: `lsof`, `netstat`, `ps` and `launchctl` on macOS, `systemctl show` on Linux. `ps` is used for arguments because the kernel call behind gopsutil also returns the environment. Spaces inside arguments are lost there, so masking is weaker.

### UDP

UDP has no listening state, so a browser looks like a server. Portly says **bound UDP endpoint**, never UDP listener.

- Linux and macOS: connected sockets are left out of the list only. Inspect, stop policy and verification count every socket on a named port. The Windows table has no remote address, so connected and unconnected endpoints look the same there.
- Ports in the ephemeral range are hidden from the list, counted in the footer and in JSON, and shown with `--all`. This is noise reduction only.
- A port the user names is never hidden.

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
- Manifests are opened inside the project root only (`os.Root`), without following a final symlink and without blocking, then checked on the open descriptor: regular file, at most 1 MiB.
- Under sudo, the process owner's home is used, not root's.
- The observed working directory and the inferred project root are separate fields, and the evidence is shown. A process can change directory after it starts.
- Project name and confidence affect display only. They never permit or forbid a stop.

## 9. Docker and Podman

- Plain HTTP over a local socket or named pipe. Podman serves the same API.
- **Podman containers are listed but never stopped**, also not with `--force`. Podman 3.4 rejects `t=-1`, and 4.9.3 accepts it and kills the container within seconds, because the endless timeout overflows. An engine that is neither Docker nor Podman is treated the same way.
- Endpoint order: `DOCKER_HOST`, `DOCKER_CONTEXT`, the active context, then known paths (default, rootless, Docker Desktop, Colima, OrbStack, Podman).
- Remote engines are ignored, because their ports are not local.
- Sockets must be owned by root or the caller. Responses are size-limited.
- **A local socket does not prove a local engine:** it can be forwarded from another machine or mounted into a container. An engine is proven local on Linux when the container's init PID exists here and its cgroup carries that container's ID. A host-network process belongs to a container only if its cgroup path is the same as that init process's, because any user can create a cgroup with that name. An engine that can't be proven local this way (a VM on macOS, Windows or Linux) counts only for a port whose host socket is held by that engine's own forwarder, recognized by the engine's socket path and the program's name. Otherwise its containers are listed as unproven and can't be stopped.
- Limits: 400 ms for listing, 3 s to connect and inspect before a stop, 10 s to wait for the container to exit.
- Only host-published ports are listed. Exposed-only ports are not.
- Compose labels give the project, service and directory. Host-network containers are matched through the process's cgroup.
- A published port is not always a host listener: with the userland proxy disabled or in some network modes there may be no host socket. Such ports are listed as `published`.

### Which owner wins

1. Exactly one running local container publishes this protocol, port and host address, and it explains every socket on the port. Explained means held by its proxy or the engine, or an ownerless socket created by the engine's user whose family, address and port exactly match one of this container's published bindings. The target is that container, through the engine API. The proxy is never signalled.
2. Published by an engine proven local, with no host socket: the target is the container, verified by container state instead of a socket.
3. A socket that is not explained, several containers, or no engine answer for a forwarder: refused.
4. The container Portly itself runs in is protected.

If an engine publishes a port, Portly never falls back to signalling a native process on it.

## 10. Stop safety

### Invariants

1. Never force kill unless the user passed `--force`, and never before a graceful stop was tried and failed. Failed means the stop was accepted, the full wait passed, and the target is alive and still holds a named port. A rejected, errored or unobservable graceful stop never leads to force. The only exception is a target with no graceful method.
2. Never signal a process that was not pinned and revalidated: same process identity, holding the same socket. Where the OS has no pin, the identity is checked immediately before the signal.
3. Ambiguous, unresolved, protected, supervised or forwarder means refuse.
4. `--yes` skips only prompts. It never implies `--force`. Neither `--force` nor `--expect` skips a check.
5. A bare port means TCP.
6. Never run sudo. Print the command.
7. Success only after the target is gone (a container is not running and was not started again), its socket is gone, and the port is free twice, 1 s apart.
8. Never select a target without a named port. Always show the other known endpoints and child processes a stop affects. For a container, also show containers sharing its network, that a paused one will be resumed, and that a `--rm` one will be removed.
9. A target refused under one named port is not stopped through another.
10. Names and project confidence never grant permission to stop.
11. A container is stopped only through an engine proven local, and never the one Portly runs in.
12. Names in printed commands are quoted or left out.

### Policy

| Situation | Result |
|---|---|
| One resolved owner | Allowed |
| Verified family (below) | Allowed, only the root is signalled |
| Plain reloader: parent has the same executable, arguments after the program name, directory and user, and no socket | Allowed as one resolved owner, and shown as a reloader |
| One verified container (section 9) | Allowed, through the engine API |
| Unrelated owners, or native plus container | Refused: ambiguous |
| Incomplete evidence that could change the target (below) | Refused: unresolved, or permission denied |
| PID 1, kernel, Portly and its ancestors, system processes | Refused: protected |
| docker-proxy, rootlessport, rootlesskit, pasta, passt, slirp4netns, vpnkit, conmon, Docker Desktop (`com.docker.backend`), Lima, Colima, gvproxy, OrbStack, wslrelay, sshd, and ssh started by a VM manager | Refused: forwarder |
| nodemon, pm2, supervisord, runit, s6, air, watchexec, `node --watch`, systemd, launchd, Windows services | Refused: supervised, with the right command where it is safe to print one |
| Swarm, Kubernetes or systemd-managed container | Refused |
| Container of Podman or an unknown engine | Refused |
| Container whose stop signal is not known to be graceful | Refused without `--force` |
| Being debugged, or suspended while SIGTERM is ignored or blocked | Refused: unsupported |
| Native Windows process | Refused without `--force`: no graceful method |
| A platform whose evidence gate has not passed (section 16) | Refused: unsupported |

The policy is a pure function: snapshot in, stop plan out. It reads no clock and no OS state, so every rule can be tested directly.

- **Protection is structural first:** PID 1, kernel threads, ancestry, cgroup unit, launchd job, service manager, session 0, engine mapping. Name lists age, so they only add refusals. A missing name never turns a structural refusal into permission.
- **Verified family (Linux):** every holder of the socket has the same user and executable, all descend from one root through other holders, and the root catches SIGTERM. Being an ancestor is not enough: a package runner that does not hold the socket is never signalled. Catching SIGTERM does not prove a clean exit, so verification decides. Families are refused on macOS and Windows.
- **Plain reloader:** the parent normally exits with its child. If the listener comes back within the verification window, the outcome is `restarted`, never success.
- **System process:** without a service manager Portly can query, a process with no controlling terminal whose user is a system account is protected. Absence of evidence is not permission.
- **Named supervisors** are matched on the ancestors up to a session leader that has a controlling terminal, otherwise up to PID 1, so wrappers and `setsid` don't hide them. PID 1 and per-user managers are judged only by the two rules below.
- **systemd:** an innermost cgroup unit of type `.service` means supervised. A `.scope`, such as a terminal or login session, does not. Exception: a process with a controlling terminal that is neither the unit's main process nor its direct child is not supervised by it, because shells started inside an application share its unit.
- **launchd:** query the system domain and the owner's user and GUI domains. An unreadable domain blocks. Supervised means the listener or an ancestor is the main process of a job. An application job (`application.*`) counts only if the listener is that main process. Being reparented to launchd is not supervision. **[to verify]**
- **Printed commands:** a stop command is printed only if the listener is the unit's main process or its direct child, and the unit is not ssh, cron, a user manager or the desktop session. Otherwise a status command is printed. Names in printed commands are quoted, or left out if they contain anything but letters, digits and `@._:-`, or start with `-`.

### Incomplete evidence

Listing accepts partial data. Stopping is blocked only by missing evidence that could change the target or its safety:

| Blocks the stop | Does not block |
|---|---|
| Socket table unreadable or not parseable | No engine socket, connection refused, or an engine run by another user |
| An engine socket owned by the process's user that does not answer in time | |
| A socket on that protocol and port without a known owner | Unreadable processes unrelated to the owner |
| No visible holder has the socket's user | Unknown project or directory |
| Sockets or owners of the named port still changing after one rescan | Unreadable command line |
| A descendant of the owner whose sockets can't be read | |
| Owner in a container, or a forwarder, without an engine mapping | |

Blocking on every unreadable process would refuse every stop by a normal user, because other users' processes are always unreadable. The remaining case is an unrelated hidden process holding the same socket. The port then stays held, which is reported and never counted as success.

### Several ports

One command makes one plan. All named ports are resolved, grouped by target and decided per target. The plan is shown and confirmed once. Each target is signalled at most once, one at a time, revalidated right before its own signal. A refused target does not block other targets. A process that is a possible owner of a refused port is refused for every named port, and so is its parent.

### Expected target

`portly stop <ports> --json` without `--yes` stops nothing and returns a `plan_id`. Passing it back with `--expect` makes the stop fail with `target_changed` if the plan differs.

- The id is a truncated SHA-256 (128 bits) over the allowed targets: their identity, family members, named endpoints, other listening TCP and non-ephemeral UDP endpoints, and whether they are suspended or paused. Ephemeral UDP is left out because it changes constantly.
- A mismatch refuses the whole command. An empty or malformed id is a usage error. Every normal check still runs.
- Before a force kill only target identity is compared, because the graceful stop may already have closed some sockets. The target must still hold at least one named socket, or be a running container.
- It is not a secret and authorizes nothing.

### Revalidation and executors

1. Pin the target.
2. Take a new snapshot of the named ports, never the one from before the prompt.
3. Run the policy again and compare with the plan.
4. Read the process identity again and find the same socket in that process.
5. **Check the pin is still alive.** Reading by PID proves nothing about the pinned process unless the pin is alive afterwards.
6. Signal. Nothing slow runs between steps 4 and 6.

A target that gained another listening endpoint (ephemeral UDP excluded), was suspended or was paused since the prompt counts as changed.

| | Linux | macOS | Windows | Container |
|---|---|---|---|---|
| Pin | pidfd | not available | process handle | ID and start time |
| Graceful stop | SIGTERM | SIGTERM | none | `POST /stop?t=-1` |
| PID reuse race | none with pidfd, tiny on kernels before 5.3 | tiny | none, the handle prevents it | not applicable |
| Ownership race | tiny | tiny | tiny | tiny |

- **Ownership race:** no OS ties a signal to a socket. A process can release the port in the instant between the last check and the signal. The process signalled is still the one that was shown and confirmed.
- **PID reuse on macOS and old kernels:** the PID would have to be freed and handed out again within that same instant. Accepted and documented.
- **Suspended processes (Linux, macOS):** a suspended process can't handle SIGTERM, so SIGCONT follows it. This is never silent: the prompt says "suspended, will be resumed to stop" and JSON has `resumed`.
- **Windows:** Ctrl+C can only be sent to every process on a console, including the user's shell, and the console's members can change before it arrives. Portly does not use it.
- **Containers:** the engine's default stop ends in a force kill, so Portly passes `t=-1`. If an engine rejects that value, the stop is reported as unsupported and **never retried without it**. A container that ignores the signal keeps running and the request stays pending at the engine, even after Portly disconnects. Portly says so and suggests `--force`.

### Force kill

A last resort for a process that ignores a graceful stop. It can lose unsaved data.

```
  graceful stop delivered, full wait over,
  target still holds the port
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
  | Pin, new snapshot, policy      | ------> refused, exit 5
  +--------------------------------+
                 | same target, still holding
                 v
  +--------------------------------+
  | Kill, then verify port is free |
  +--------------------------------+
```

| | Linux | macOS | Windows | Container |
|---|---|---|---|---|
| Force kill | SIGKILL through the pidfd, or by PID on kernels without it | SIGKILL | `TerminateProcess` on the pinned handle | engine API `kill` |

- Only `still_listening` by the target itself, or `pending`, continues to force. `draining`, `restarted`, `taken_over` and `unknown` are reported.
- `--force` alone asks twice, or once when there is no graceful method. The second question needs the word `kill`, so a `y` typed while waiting is not an answer. `--force --yes` asks nothing. `--yes` alone never kills. Scripts should add `--expect`.
- Every refusal in the policy table still applies, apart from the two rows that name `--force`. Ambiguous, protected, supervised, forwarder and unresolved targets can't be forced.
- If there is no graceful method (a native Windows process, a container whose stop signal is a force kill), `--force` goes straight to its confirmation. Nothing graceful was possible, and Portly says so.
- **Families:** killing only the root would leave workers holding the port. Portly shows and pins every member that holds the socket, kills the root first, then the members still holding it. Processes that don't hold the socket are never killed. If the root already exited, its workers are not killed, because the confirmed root can no longer be verified.
- JSON reports `"method": "graceful"` or `"method": "forced"`, plus the PIDs that were killed.
- Without a terminal, `--force` needs `--yes`. Otherwise the result is `confirmation_required`.

### Verification

Poll for 5 s (containers 10 s). Process and port are reported separately, as `process_exited` and `port_released`.

| Outcome | Meaning |
|---|---|
| `stopped` | Target gone and port free, both again after 1 s |
| `draining` | Port free, process still running |
| `still_listening` | The target, or a holder that was not visible, still has the socket |
| `restarted` | The same program listens again in a new process |
| `taken_over` | Another program listens now |
| `pending` | Container engine accepted the stop, container still running |
| `unknown` | The result could not be observed |

A restart more than 1 s after the port became free is not detected.

## 11. Security

Portly may run as root. Assume a hostile local user controls process names, arguments, directories, manifests, container labels, environment and process lifetimes.

| Threat | Defence |
|---|---|
| PID reuse or exec between check and signal | Pin, resolve again, compare identity, signal through the pin |
| Socket changes hands while the prompt is open | The same socket must still be held by the pinned process |
| Target changes between an agent's inspection and its stop | `--expect` with the plan id |
| Listener restarted and reported as stopped | Free twice, 1 s apart, then compare programs |
| Wrong target through shared or hidden sockets | Refuse unless every socket on the port resolves to one target with sufficient evidence |
| Force kill used by accident or by an agent | Explicit `--force`, graceful first where one exists, second confirmation, all checks rerun |
| Engine force-killing on Portly's behalf | `t=-1`, never retried without it |
| Planted binaries on PATH | Absolute paths, no shell, minimal environment |
| Manifest that is a symlink, FIFO, device or huge file | Open inside the project root, no-follow, non-blocking, check the descriptor, size limit, deadline |
| Terminal escape or bidi sequences in names | All process-derived text is sanitized |
| Secrets in command lines | Masked in terminal and JSON, best effort. Environments are never read |
| Planted engine socket or hostile `DOCKER_HOST` | Local only, owner check, bounded responses |
| Forwarded or mounted engine socket of another machine | Engine must be proven local before a container can be stopped |
| Hostile unit, label or container name in a printed command | Quoted or left out |
| Slow disks, huge process tables | Deadlines, partial results |

Failure always means refuse. Unknown is never treated as safe.

**Limits:** secret masking is pattern-based and can miss unusual formats. Root and the OS are trusted. The ownership race and, without a pin, the PID race in section 10 remain.

## 12. Output contract

- stdout is data, stderr is diagnostics.
- `--json` prints exactly one document, errors included, without colour or prompts. Inaccessible fields are `null`.
- `schema_version` is `"1"`. Within 1.x fields may be added, never changed or removed.
- `stop --json` without `--yes` returns `confirmation_required`, the targets and a `plan_id`, which works as a dry run.
- Stop output is a `results` array with one entry per named port, each pointing to its target. Targets list affected endpoints, evidence and outcome.
- Every refusal has a stable reason code, so results that share an exit code stay distinct.
- Incomplete discovery still gives a valid document, with completeness per endpoint.
- The JSON schema and reason codes are written before the code that produces them.
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
| 7 | Signal sent or force kill declined, but the result is `draining`, `still_listening`, `restarted`, `taken_over`, `pending` or `unknown` |
| 8 | Several ports with different outcomes |

## 13. Platforms

| Tier | Targets | Means |
|---|---|---|
| Supported | Linux amd64 and arm64, macOS 13+ amd64 and arm64, Windows amd64 | Full test suite runs natively on every change |
| Tested in part | Linux 386, armv6, armv7, s390x; Windows 386 and arm64 | Unit tests, natively or emulated. s390x covers big-endian parsing |
| Build only | Linux riscv64 and ppc64le | Compiled and vetted. Release notes say so |

Go no longer supports 32-bit macOS or 32-bit ARM Windows. Binaries are static, so they don't depend on the Linux distribution's C library.

| Capability | Linux | macOS | Windows |
|---|---|---|---|
| List and inspect | Full | Full | Full, connected UDP not distinguishable |
| Graceful stop, single owner | Yes | Yes, after its gate | No |
| Graceful stop, family | Verified families | No | No |
| Force kill | Yes | Yes, after its gate | Yes, after its gate |
| Docker containers | Yes | Yes | Yes |
| Podman containers | List only | List only | List only |

## 14. Quality

| Layer | Proves |
|---|---|
| Architecture tests | Import direction, library boundaries, comment limit |
| Unit and golden | Parsers, detection, rendering, JSON against the schema, also with incomplete discovery |
| Policy | One test per rule in section 10, plus random snapshots that must never select a protected process |
| Fuzz | Every parser of untrusted data, masking, arguments |
| Races | Process or socket replaced between prompt, pin and signal; owner exits during a scan; binding and releasing under load |
| End to end | Graceful stop, ignored signal, force kill (never without `--force`, refusals still hold), respawn, suspended process, dual stack, two owners, UDP, several ports |
| Containers | Throwaway Docker and Podman containers |
| Distributions | Alpine, Debian, Fedora, Arch, openSUSE |
| Budgets | Time, memory, 5,000-process scan |
| Static analysis | gofmt, vet for each OS, staticcheck, govulncheck, race detector |

Coverage target: 85% overall, every branch of the stop policy. Coverage is not the release criterion: **every invariant in section 10 needs a test where it allows, one where it refuses, and one where the state changes in between.**

Required scenarios:

| Scenario | Result |
|---|---|
| PID exits and is reused | The new process is never signalled |
| Owner closes the socket after confirmation | Refused |
| Two processes share a port with `SO_REUSEPORT` | Refused, unless a verified family |
| One process owns 3000 and 3001 | Both shown, signalled once |
| A target refused under one named port, allowed under another | Not stopped |
| Conflicting socket with unreadable owner | Refused: unresolved |
| Listener is restarted within the verification window | `restarted`, never success |
| docker-proxy and its container on one port | Container, or refused |
| Container ignores the stop signal | `pending` |
| Engine rejects `t=-1`, also with `--force --yes` | Refused, no second request, no kill |
| Engine socket forwarded from another machine | Refused |
| Target closes the port during the second prompt | Not killed |
| Published container and an unrelated socket on another address | Refused: ambiguous |
| Two UDP sockets on one port, one connected | Refused: ambiguous |
| Dev server in a terminal inside a desktop application unit | Allowed |
| Dev server whose parent became launchd | Allowed |
| Unit name with shell characters | Not printed in a command |
| Plain stop on a Windows process sharing a console | Refused, no console event |
| Suspended process | Shown as resumed; refused if SIGTERM is ignored |
| Working directory changes or disappears | Lower confidence, same stop decision |
| Manifest is a symlink or FIFO | Not followed, does not block |
| `--expect` after the target was replaced | Refused: `target_changed` |

## 15. Build and release

- **CI:** on push to `main` and on pull requests. Builds every target and runs the tests of section 13's tiers on Linux, macOS and Windows, plus distribution containers. Read-only token, no secrets on pull requests, actions pinned by SHA.
- **Release:** on a `v*` tag only, with GoReleaser at a pinned version. It produces archives, deb, rpm, apk and Arch packages with man page and completions, signed checksums and SBOM on GitHub Releases. Build attestation is a separate workflow step. Locally it only runs in snapshot mode, which publishes nothing.
- Binaries are never committed. `resources/releases/` is a local folder.
- SemVer. Section 12 is the compatibility contract.

## 16. Evidence gates and resolved points

Stopping is enabled on a platform only after its gate passes there. Until then stop is refused with `unsupported`, and listing still works.

| Gate | Must prove |
|---|---|
| Linux | Families (gunicorn, `uvicorn --reload`, node cluster) and reloaders (Django, Flask) with real servers; behaviour without pidfd, without systemd, with restricted `/proc`, and in other namespaces |
| macOS | The `lsof` and `netstat` cross-check finds hidden owners across users, shared sockets and overlapping addresses; a stable socket id exists; launchd domains can be read and jobs are told apart from applications |
| Windows | A socket inherited by a child is not attributed to a parent alone; bind time detects reused PIDs, with a creation time close to the bind time treated as unresolved; the pinned handle is the process that is terminated |
| Docker | `t=-1` never escalates, engines that reject it are refused, locality is proven, and the proxy, no-proxy and routed modes resolve as in section 9 |
| Podman | Failed: `t=-1` ends in a kill. Stopping stays refused |

| # | Point | Resolution |
|---|---|---|
| 1 | Process families on macOS | Refused, also with `--force` |
| 2 | Windows graceful stop | None. Plain stop refuses, `--force` is explicit |
| 3 | UDP stop | Only with `/udp` |
| 4 | Ambiguous owners | Refused. Choosing a PID could come later |
| 5 | macOS text parsing of `lsof` and `netstat` | Discovery yes, proof of safe stopping only through its gate |
| 6 | Owner unresolved shares exit 4 with permission denied | Kept, reason codes differ |
| 7 | CI trigger | `main` and pull requests |
| 8 | UI libraries | Cobra and Lip Gloss only |
| 9 | Name lists | Supplemental. They only add refusals |
| 10 | systemd | Cgroup unit decides, except for processes on a terminal inside the unit |
| 11 | `--force --yes` unattended | Kept, every check applies, `--expect` available |
| 12 | Force kill of a family | Verified families only, members holding the socket |
| 13 | Supervised processes with `--force` | Refused, the supervisor would restart them |
| 14 | Automatic SIGKILL | Never. Escalation only after `--force` |
| 15 | Suspended processes | Resumed to stop, always disclosed |
| 16 | macOS PID reuse race | Accepted and documented |
