# v0.1.1 (unpublished)

- Persistent Make-based hub and Linux systemd receiver lifecycle, available in source and Linux release archives.
- File-only registry credentials, loopback-only hub, explicit HTTP opt-in, and receiver mTLS credential delivery through systemd.
- Independent removable Caddy wrapper with distinct server/client certificate authorities and per-client enrollment.
- Linux bundles include Makefile, environment examples, runtime templates and operator documentation. Existing v0.1.0 assets are unchanged.

# Revision 2

- Agent HTTP requests carry X-Edgelab-Device so hub per-device accounting covers sync/watch traffic (v3.1 sustainability work), not just push dialers.
- Main real Docker demo with shared layers, intra-layer edit, faulty delivery, process restart, import and offline payload probes. Execution on a Docker host remains pending in the supplied evidence.
- Sender-only collection client, persistent SQLite history, CLI watch view, JSON/CSV reports, and durable outbound staged/loaded acknowledgements.
- Three native editable Excalidraw scenes with SVG previews.
- OpenSpec baseline requirements, active proposal/design/specs/tasks, and an offline structural checker.
- Plain-English README with primary commands, evidence meanings, failure behavior and explicit limits.
- Updated transport evidence, sender collection data and tests. Original evidence retained separately.
