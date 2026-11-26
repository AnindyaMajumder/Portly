# Changelog

## 0.8.0 (2026-11-27)

First public release.

- List and inspect TCP and UDP ports with their process, container and project.
- In a terminal, the list is a bordered table that keeps the directory over the address when space is short, and long directories are shortened in the middle. Narrow terminals, `TERM=dumb` and piped output get plain columns; piped output keeps full paths.
- Stop by port with a plan, confirmation, revalidation and verification. Switched off until each platform's evidence gate has passed.
- Docker and Podman containers through their local engine. Podman containers are listed, not stopped.
- JSON output, exit statuses and reason codes, version 1.
- `portly update` replaces the binary with the latest verified release, after asking.
- Install, update and uninstall scripts for macOS, Linux and Windows. Packages for apt, dnf, zypper, apk and pacman.
