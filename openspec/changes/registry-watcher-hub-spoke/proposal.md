# Registry watcher, hub/spoke delivery, notifications, TUI and packaging

## Why
The v2 lab proves the signed chunk transport end to end with real Docker evidence, but
delivery is still driven by hand: a human publishes, one simulated device pulls, and the
sender has no production surface. The user asked to evolve this toward a production
delivery system: watch an OCI registry for newly published images, notify via Telegram
and Slack, serve MANY clients from ONE sender (hub and spoke) with multiplexing and
caching, push announcements over websockets, observe the daemon with a TUI, package the
sender as a container with a Helm chart, and give clients a portable lightweight binary
with a configuration file and explicit action modes.

## What Changes
- Add a registry watcher (`edgelab watch-registry`) that polls any Docker Registry v2 API
  endpoint (Harbor compatible), tracks tag→manifest-digest changes, applies per-repo
  regex allowlist and ignorelist, persists last-seen digests, and triggers the publish
  pipeline for new digests.
- Add a notifier layer with Telegram bot API and Slack webhook implementations, covering
  release-published, delivery-staged, delivery-loaded, delivery-failed and watcher-error
  events, with retry and backoff.
- Evolve the origin server into a hub: serve many concurrent clients with singleflight
  chunk reads, a bounded LRU of hot encoded chunks, and per-client served-bytes metrics.
- Add a push-only websocket endpoint `/events` announcing new signed releases; actual
  transfers stay plain HTTP with existing ETag/304 polling as the fallback.
- Add an admin unix-socket JSON endpoint on the daemon (live stats, per-device
  acknowledgements, receipts, connected clients) and a separate `edgelab-tui` binary
  that renders it live; keep JSON/CSV export commands in Go.
- Package the sender daemon as a multi-stage container image with a Helm chart
  (persistence, admin socket, notifier secrets, resources) and plain docker-run docs.
- Add client configuration (YAML) with release allowlist/ignorelist, action mode
  `none` (dry-run default) | `load` | `restart`, cache dir, origin URL and pinned keys;
  `restart` maps the new image ID to affected containers and recreates them.
- All new production surface is Go. Existing Python demo/smoke harnesses remain as lab
  scaffolding and historical evidence; sender.py stays as evidence, not production code.

## Capabilities

### New Capabilities
- `registry-watcher`: implementation-agnostic OCI registry polling with digest tracking,
  allowlist/ignorelist filtering and publish triggering.
- `notifications`: Telegram and Slack delivery of lifecycle events with retry/backoff.
- `hub-multiplex`: one sender serving many clients without thundering-herd disk reads.
- `push-channel`: push-only websocket announcements with HTTP poll fallback.
- `admin-tui`: unix-socket admin API and a live terminal observer binary.
- `packaging`: container image and Helm chart for the sender daemon; client build targets.
- `client-config`: configuration file and explicit action modes for the lightweight client.

### Modified Capabilities
None. The signed chunk-transfer format, signature verification, durable-commit,
disk-preflight, cache revalidation and whole-artifact hash invariants from v2 are
consumed unchanged. The origin HTTP object paths and manifest verification path are
reused, not replaced.

## Impact
Go gains new packages under `internal/` and new subcommands plus one new binary
(`edgelab-tui`). New third-party dependencies: a websocket library (gorilla/websocket)
and a TUI library (Bubble Tea); go-containerregistry only if stdlib registry token auth
proves impractical. Docker and Helm are exercised only against the local OrbStack
daemon and local `helm lint`/`template`; no cluster deployment is claimed. No live
Harbor and no real Telegram/Slack credentials are required: httptest fakes are the
unit proof, and anything not executed is recorded NOT RUN in PRODUCT_RESULT.md.
