# Portly

See what uses your local ports, which project it belongs to, and stop it safely. One binary, no configuration.

```
$ portly
PORT  PROTO  PROJECT  TYPE    PID   PROCESS            ADDRESS    DIRECTORY
22    tcp    Unknown  Native  —     Permission denied  0.0.0.0    —
5432  tcp    shop     Docker  —     shop-db-1          127.0.0.1  ~/shop
8000  tcp    shop     Python  6182  uvicorn            127.0.0.1  ~/shop
```

| Command | Does |
|---|---|
| `portly` | List listening TCP and bound UDP ports |
| `portly --all` | Also list likely-client UDP ports |
| `portly 8000` | Details for a port, or several |
| `portly stop 8000` | Show exactly what would stop, ask, stop it gracefully |
| `portly stop 5353/udp` | Stop a UDP owner. A bare port means TCP |
| `portly stop 8000 --yes` | Do not ask. Every check still runs |
| `portly stop 8000 --force` | Graceful first. If that fails, ask again and kill |
| `--json` | One JSON document, for scripts and agents |

## Status

Listing and lookup work on Linux. macOS and Windows are built and unit tested, and not yet confirmed on real machines.

**Stopping is switched off in this version on every platform.** Each platform has an evidence gate, a set of tests against real servers, and stopping is enabled only after its gate has passed there. Until then `portly stop` explains that and stops nothing.

## Install

There is no release yet. To build it, use Go 1.27 or later:

```
make build
bin/portly
```

Once releases exist, `resources/install.sh` (Linux, macOS) and `resources/install.ps1` (Windows) install one for the current user and verify its checksum.

## Safety

Portly refuses rather than guesses. It never stops a port with several owners, a system or supervised process, a port forwarder, or a container it can't prove is local. It never runs sudo and never reads other processes' environment variables. Secrets in command lines are masked, best effort. A kill happens only with `--force`, after a graceful stop was delivered and failed.

## For scripts and agents

```
portly stop 8000 --json                          # stops nothing, returns a plan_id
portly stop 8000 --yes --json --expect <plan_id> # stops only those same targets
```

Output, reason codes and exit statuses are described in [resources/json.md](resources/json.md). The design is in [resources/ARCHITECTURE.md](resources/ARCHITECTURE.md).

## License

MIT
