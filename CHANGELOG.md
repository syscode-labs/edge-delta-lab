# v0.1.2 (unpublished superseding patch)

- Target a new patch release for the container build-context regression: the source-build Dockerfile omitted the local `hubclient` package required by `cmd/edgelab`. Both v0.1.1 container jobs failed; archive publication succeeded independently.
- Advance chart metadata and documented download names to v0.1.2. Publication, container manifests and hosted acceptance of v0.1.2 remain pending; local metadata is not delivery proof.
- Retain anonymous hosted v0.1.1 archive acceptance centrally in [TESTING.md](TESTING.md#hosted-v011-acceptance-and-v012-delivery-status). Do not replace, retag or rebuild over existing v0.1.1 or v0.1.0 assets.

# v0.1.1 (archives published; container publication failed)

- Persistent Make-based hub and Linux systemd receiver lifecycle, available in source and Linux release archives.
- File-only registry credentials, loopback-only hub, explicit HTTP opt-in, and receiver mTLS credential delivery through systemd.
- Independent removable Caddy wrapper with distinct server/client certificate authorities and per-client enrollment.
- Linux bundles include Makefile, environment examples, runtime templates and operator documentation. Existing v0.1.0 assets are unchanged.
- Anonymous downloads and the hosted Linux amd64 package passed all 17 packaged Make/mTLS lifecycle gates on 2026-09-20. Arm64 was inspected, not executed. Release workflow [35509755784](https://github.com/syscode-labs/edge-delta-lab/actions/runs/35509755784) failed overall: `validate` and `github-release` succeeded, while both container jobs failed. This is partial delivery, not a fully successful release.

# Revision 2

- Agent HTTP requests carry X-Edgelab-Device so hub per-device accounting covers sync/watch traffic (v3.1 sustainability work), not just push dialers.
- Main real Docker demo with shared layers, intra-layer edit, faulty delivery, process restart, import and offline payload probes. Execution on a Docker host remains pending in the supplied evidence.
- Sender-only collection client, persistent SQLite history, CLI watch view, JSON/CSV reports, and durable outbound staged/loaded acknowledgements.
- Three native editable Excalidraw scenes with SVG previews.
- OpenSpec baseline requirements, active proposal/design/specs/tasks, and an offline structural checker.
- Plain-English README with primary commands, evidence meanings, failure behavior and explicit limits.
- Updated transport evidence, sender collection data and tests. Original evidence retained separately.
