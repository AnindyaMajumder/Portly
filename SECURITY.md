# Security

Please report vulnerabilities privately through GitHub security advisories on this repository, not in public issues.

Portly may run as root, so it assumes that another local user controls process names, arguments, directories, manifests, container labels and process lifetimes. The threats and defences are in section 11 of [resources/ARCHITECTURE.md](resources/ARCHITECTURE.md).

In scope: stopping anything other than the confirmed target, a kill without `--force`, reporting success while the port is held, running another program than intended, reading other processes' environment, terminal escapes in output, and `portly update` installing anything other than the release it verified.

Known limits: masking of secrets is pattern based. No system ties a signal to a socket, so a process can release its port in the instant before the signal. On macOS a PID could in theory be reused in that same instant. `portly update` checks the download against `checksums.txt` fetched over HTTPS from the same GitHub release. It does not check the Sigstore signature, which `cosign verify-blob` can do by hand.
