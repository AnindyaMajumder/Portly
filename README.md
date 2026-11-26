# Portly

See what uses your local ports, which project it belongs to, and stop it safely. One binary, no configuration.

Running `portly` prints a table like this one:

| PORT | PROTO | PROJECT | TYPE | PID | PROCESS | ADDRESS | DIRECTORY |
|---|---|---|---|---|---|---|---|
| 22 | tcp | Unknown | Native | — | Permission denied | 0.0.0.0 | — |
| 5432 | tcp | shop | Docker | — | shop-db-1 | 127.0.0.1 | ~/shop |
| 8000 | tcp | shop | Python | 6182 | uvicorn | 127.0.0.1 | ~/shop |

In a terminal the table has borders, and columns that don't fit are dropped from the right. Long directories are shortened in the middle, like `~/…/acme/web-app`. Piped output has plain columns and full paths.

| Command | Does |
|---|---|
| `portly` | List listening TCP and bound UDP ports |
| `portly --all` | Also list likely-client UDP ports |
| `portly 8000` | Details for a port, or several |
| `portly stop 8000` | Show exactly what would stop, ask, stop it gracefully |
| `portly stop 5353/udp` | Stop a UDP owner. A bare port means TCP |
| `portly stop 8000 --yes` | Do not ask. Every check still runs |
| `portly stop 8000 --force` | Graceful first. If that fails, ask again and kill |
| `portly update` | Update to the latest release, after asking |
| `--json` | One JSON document, for scripts and agents |

## Status

Listing and lookup work on Linux. macOS and Windows are built and unit tested, and not yet confirmed on real machines.

**Stopping is switched off in this version on every platform.** Each platform has an evidence gate, a set of tests against real servers, and stopping is enabled only after its gate has passed there. Until then `portly stop` explains that and stops nothing.

## Install

Every release is on the [releases page](https://github.com/AnindyaMajumder/Portly/releases), with checksums signed by Sigstore.

**macOS, Linux, and Windows in Git Bash or WSL**

```
curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/install.sh | sh
```

**Windows PowerShell or cmd**

```
powershell -NoProfile -Command "curl.exe -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/install.ps1 | Out-String | iex"
```

Both install for the current user, without administrator rights, and verify the checksum. They put `portly` in `~/.local/bin`, or `portly.exe` in `%LOCALAPPDATA%\Programs\Portly`, and tell you if that folder is not on your `PATH`. Set `PORTLY_INSTALL_DIR` to choose another folder, and `PORTLY_VERSION` for an older release.

### Update and uninstall

| | macOS, Linux, Git Bash | Windows PowerShell or cmd |
|---|---|---|
| Update | `portly update`, or `curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/update.sh \| sh` | `portly update`, or `powershell -NoProfile -Command "curl.exe -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/update.ps1 \| Out-String \| iex"` |
| Uninstall | `curl -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/uninstall.sh \| sh` | `powershell -NoProfile -Command "curl.exe -fsSL https://github.com/AnindyaMajumder/Portly/releases/latest/download/uninstall.ps1 \| Out-String \| iex"` |

`portly update` asks first, never downgrades, and changes nothing if the download does not match its checksum.

### Linux packages

Download the package for your system from the releases page, then:

| System | Install | Remove |
|---|---|---|
| Debian, Ubuntu (apt) | `sudo apt install ./portly_0.8.0_amd64.deb` | `sudo apt remove portly` |
| Fedora, RHEL (dnf) | `sudo dnf install ./portly-0.8.0-1.x86_64.rpm` | `sudo dnf remove portly` |
| openSUSE (zypper) | `sudo zypper install --allow-unsigned-rpm ./portly-0.8.0-1.x86_64.rpm` | `sudo zypper remove portly` |
| Alpine (apk) | `sudo apk add --allow-untrusted ./portly_0.8.0_x86_64.apk` | `sudo apk del portly` |
| Arch (pacman) | `sudo pacman -U ./portly-0.8.0-1-x86_64.pkg.tar.zst` | `sudo pacman -R portly` |

Packages include the man page and shell completions. To update, install the newer package the same way. `portly update` leaves package-managed installs to the package manager.

### Archives

`.tar.gz` for Linux and macOS, `.zip` for Windows. Each holds one binary: put it anywhere on your `PATH`. A macOS browser download is quarantined: run `xattr -d com.apple.quarantine portly` once, or use the curl command above.

### From source

With Go 1.27 or later: `make build`, then `bin/portly`.

## Safety

Portly refuses rather than guesses. It never stops a port with several owners, a system or supervised process, a port forwarder, or a container it can't prove is local. It never runs sudo and never reads other processes' environment variables. Secrets in command lines are masked, best effort. A kill happens only with `--force`, after a graceful stop was delivered and failed.

## For scripts and agents

```
portly stop 8000 --json                          # stops nothing, returns a plan_id
portly stop 8000 --yes --json --expect <plan_id> # stops only those same targets
```

Output, reason codes and exit statuses are described in [resources/json.md](resources/json.md). The design is in [resources/ARCHITECTURE.md](resources/ARCHITECTURE.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security problems: [SECURITY.md](SECURITY.md).

## License

MIT
