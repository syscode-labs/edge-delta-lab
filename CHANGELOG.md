# Unreleased documentation cleanup

- Consolidate packaged operations at `OPERATIONS.md` and native/Helm alternatives at `docs/DEVELOPMENT.md`.
- Merge design references into architecture and synthetic-test guidance into `TESTING.md`; remove redundant compatibility pages from source and future bundles.
- Correct current release guidance. Existing tags, release assets and dated evidence remain unchanged.

# v0.1.2

- Include the required local `hubclient` package in the source-build Docker context, fixing v0.1.1 container publication.
- Publish v0.1.2 archives, chart and daemon/client containers. [TESTING.md](TESTING.md#hosted-v012-acceptance-and-complete-release-delivery) owns publication records, hosted acceptance and platform limits.

# v0.1.1

- Add persistent Make-based hub and Linux systemd receiver lifecycle, file-only registry credentials, loopback-only hub and explicit HTTP opt-in.
- Add receiver mTLS credential delivery through systemd and an independent removable Caddy wrapper with separate server/client authorities.
- Include Makefile, environment examples, runtime templates and operator documentation in Linux bundles.
- Archives were published; container publication failed and was repaired in v0.1.2. See [historical delivery status](TESTING.md#historical-hosted-v011-acceptance).

# Revision 2

- Add real-image fault/restart/import test tooling and sender-only SQLite history, reports and outbound acknowledgements.
- Add device accounting, OpenSpec requirements and historical editable Excalidraw scenes.
- Preserve original evidence; current results and limitations live in [TESTING.md](TESTING.md).
