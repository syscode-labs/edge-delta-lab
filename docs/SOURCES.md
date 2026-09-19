# References

Use these references to understand the design choices and Docker commands behind the lab; use [the experiments guide](EXPERIMENTS.md) for what this implementation has actually demonstrated.

These sources were checked during repository preparation on 2026-09-17. They do not certify the lab's implementation, durability or performance. Measured results come from this repository's code and test scripts, not from upstream documentation.

## How can an update reuse parts of a changed file?

- [Content-defined chunking research](https://www.usenix.org/conference/atc16/technical-sessions/presentation/xia) — choosing where to split a file into pieces (chunks) from its content, so an insertion need not invalidate every later piece. The lab is not a full FastCDC implementation.
- [desync releases](https://github.com/folbricht/desync/releases) — release history for a maintained chunking/distribution tool; this lab does not implement its format.
- [desync v1.1.3 store interface](https://raw.githubusercontent.com/folbricht/desync/v1.1.3/store.go) — how it stores reusable content, not how this lab's cache (saved content for reuse) works.
- [desync v1.1.3 publisher command](https://raw.githubusercontent.com/folbricht/desync/v1.1.3/docs/cli/desync_make.md) — how desync creates its own published content.

## How does the lab sign release details?

- [Go Ed25519 documentation](https://pkg.go.dev/crypto/ed25519) — the signing and verification API. Using it does not supply fleet enrollment or a complete trust-management system.

## What does Docker loading or running an image do?

- [Load a local Docker archive](https://docs.docker.com/reference/cli/docker/image/load/) — import an image; loading does not start the application.
- [Run a Docker container](https://docs.docker.com/reference/cli/docker/container/run/) — includes pull policy and network selection used by the offline execution test.
- [Start or update services with Docker Compose](https://docs.docker.com/reference/cli/docker/compose/up/) — application activation options, not evidence that this lab implements health-gated activation.
