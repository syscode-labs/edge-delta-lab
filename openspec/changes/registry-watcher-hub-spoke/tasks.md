# Implementation and validation tasks

## 1. Registry watcher
- [x] 1.1 Registry v2 client: tags/list pagination, manifest digest resolution, bearer-token auth against httptest registry.
- [x] 1.2 Allowlist/ignorelist regex filtering and first-seen/digest-change emission.
- [x] 1.3 Persistent last-seen state (atomic JSON), no re-emission across restart.
- [x] 1.4 Publish trigger: registry→archive export feeding `lab.Publish` + promote unchanged.
- [x] 1.5 `edgelab watch-registry` subcommand with config file, poll interval, watcher-error events.
- [x] 1.6 Unit tests: token auth, pagination, allow/ignore, digest change, restart persistence, error backoff.
- [x] 1.7 NOT RUN ledger entry for live Harbor if unavailable, with reason. (No live Harbor/registry with pull credentials available in this environment; watcher validated against an in-process httptest v2 registry only. Recorded in Stage-1 evidence.)

## 2. Notifications
- [x] 2.1 Notifier interface, Event model, fan-out dispatcher that never blocks delivery.
- [x] 2.2 Telegram bot API sender (env token, chat id, plain text).
- [x] 2.3 Slack webhook sender (env webhook URL, JSON body).
- [x] 2.4 Retry with exponential backoff + jitter; honor 429 Retry-After.
- [x] 2.5 httptest test: body shape, retry-then-success, Retry-After, fan-out isolation.
- [x] 2.6 Wire notifier config into sender and client configs; events emitted on publish/stage/load/fail/watcher-error. (Emission wired; live Telegram/Slack delivery NOT RUN — no real credentials, mock-only capture.)

## 3. Hub multiplex + cache
- [x] 3.1 Singleflight wrapper for object reads over the v2 origin engine.
- [x] 3.2 Byte-budgeted LRU of encoded chunks with configurable size.
- [x] 3.3 Per-client (X-Edgelab-Device) served bytes/requests/last-seen metrics.
- [x] 3.4 Preserve v2 ETag/304, range and injected-fault behavior; v2 tests still pass.
- [x] 3.5 N=50 concurrent-client load test proving one underlying read per cold object and correct per-client byte counts.

## 4. Push channel
- [x] 4.1 `/events` websocket endpoint with announce-on-publish/promote (push only).
- [x] 4.2 Client dialer with reconnect + exponential backoff.
- [x] 4.3 Poll fallback on `releases/desired.json` with ETag/304.
- [x] 4.4 Integration test: publish → connected client syncs within one interval without polling.
- [x] 4.5 Test: forged/unknown announce causes no state change without signed manifest agreement.

## 5. Admin socket + TUI
- [x] 5.1 Unix-socket NDJSON admin endpoint (stats, clients, receipts, events, watch-status), 0660, stale-socket cleanup.
- [x] 5.2 Size-capped fsynced JSONL event log, queryable after restart.
- [x] 5.3 `edgelab serve --admin-socket` wiring.
- [x] 5.4 `edgelab-tui` Bubble Tea live view (btop-style) reading the socket.
- [x] 5.5 `edgelab-tui --json`/`--csv` one-shot exports (no TTY required).
- [x] 5.6 Tests: admin request/response, event log rotation + restart query, export modes.

## 6. Packaging
- [x] 6.1 Multi-stage Dockerfile (daemon + client targets, static, non-root, volumes).
- [x] 6.2 Helm chart deploy/helm/edgelab-hub (image, persistence, admin socket, notifier secrets, resources, service).
- [x] 6.3 `helm lint` + `helm template` smoke green. (helm 3.14.1; re-verified green at closeout.)
- [x] 6.4 Docker run docs (plain English, plain docker path first). (docs/DOCKER_RUN.md; plain docker path first, Helm second.)
- [x] 6.5 Container smoke run on OrbStack (healthz + client sync from container-served origin). (Docker 29.4.0 linux/amd64: daemon up as uid=100 edgelab, /healthz=ok, host client staged 53 chunks / 4200227 bytes through container-served origin, admin stats/events answered in-container via unix socket; smoke container+volume removed — commit d2158ce.)
- [x] 6.6 Real disposable Kind Helm lifecycle and signed client verified. (`evidence/helm-kind-lifecycle/signed-run-3/result.json`: fresh install, host-signed PVC publication, public-key-only in-cluster watch staging with exact archive SHA-256/size, origin/state persistence through exporter upgrade, pod restart and rollback, uninstall, cluster/image cleanup all passed. `validation.json` records closeout checks. Single-node local-path synthetic archive proof only; not Docker activation or production storage durability. Uninstall destructively deletes chart-managed PVCs.)

## 7. Client config + actions
- [x] 7.1 YAML config loader (origin, manifest, key, dirs, allow/ignore, action, notifiers).
- [x] 7.2 Dry-run default reporting would-do actions without touching Docker.
- [x] 7.3 `load` mode behind full verification (reuses v2 docker-load path and image-ID checks).
- [x] 7.4 `restart` mode: image→container mapping and recreate with per-container audit records.
- [x] 7.5 Allowlist/ignorelist gating before download.
- [x] 7.6 `make release-binaries` cross-compile linux/amd64+arm64, darwin/arm64.
- [x] 7.7 Tests: config parse/validate, gating, dry-run no-docker proof, restart mapping on a fake docker CLI.

## 8. End-to-end proof
- [x] 8.1 E2E driver: fake registry → watcher → publish → hub announce → N simulated spokes converge. (cmd/v3e2e: 3 sim spokes staged in ~11.6s via /events push, no polling.)
- [x] 8.2 One real docker-load spoke on OrbStack with image-ID and payload verification. (Loaded image ID inspect resolves; saved config bytes hash == signed image_ids[0] sha256:c8051b57…; staged artifact sha256 == manifest artifact_sha256 b4917d4e…; verified 13.2s after publish. Evidence: evidence/v4-productization/v3-e2e/.)
- [x] 8.3 Notifications captured at test endpoints during the run. (hub-announce promote event captured at mock endpoint, seq 1 — mock-only; live Telegram/Slack remains NOT RUN per 2.6.)
- [x] 8.4 Timing capture: publish→announce→spoke-sync-start→spoke-verified. (work/v3-e2e/TIMING.json copied to evidence/v4-productization/v3-e2e/TIMING.json: total wall 16.6s, publish→sim-spokes-synced 11.6s, publish→real-spoke-verified 13.2s, re-announce sequence 2.)
- [x] 8.5 evidence/v4-productization/ artifacts + PRODUCT_RESULT.md gate table (PASS/FAIL/NOT RUN per stage) at workspace root. (Artifacts committed; PRODUCT_RESULT.md written at closeout.)
- [x] 8.6 CI workflow update covering new packages and gates. (`.github/workflows/test.yml` runs `make check`, `make openspec-check`, transport demo and real Docker E2E. Public initial run https://github.com/syscode-labs/edge-delta-lab/actions/runs/35475144442 at `cf508ac677f8486004d2bcdca67d87cf88da5332` passed both jobs; verified via GitHub API. This is not CI evidence for the later unpushed Helm amendment.)
