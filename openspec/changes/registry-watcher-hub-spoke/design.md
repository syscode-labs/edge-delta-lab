# Design: registry-watcher-hub-spoke

## Context
V2 provides: signed immutable releases (ed25519 envelope over a chunk manifest),
content-defined chunking with independent compression, durable staged delivery with
fsync/rename/dirsync, disk preflight, cache revalidation, whole-artifact hash check,
explicit `--docker-load` behind full archive verification, an origin HTTP server with
injected faults, and outbound device receipts. The sender collector is Python
(sender.py) reading sender-side endpoints; it stays as lab evidence.

V3 adds a production surface in Go. Decisions below are recorded in the delivery brief
and are not re-litigated here.

## Goals / Non-Goals
- Goals: registry watching, notifications, hub multiplex+cache, push announcements,
  admin socket + TUI, container/Helm packaging, configurable client with action modes,
  one measured end-to-end proof with a real docker-load spoke on OrbStack.
- Non-Goals: cluster deployment, Harbor installation, tenancy/auth model (tracked as v2
  task 2.7), format changes, activation/rollback semantics (loading is not activation).

## Decisions

### D1. Websockets are push-only; transfers stay plain HTTP
The hub announces "a new signed release exists — go read releases/desired.json" over
websocket. Chunk/manifest bytes continue over HTTP GET with ETag/304 and range-friendly,
retryable semantics from v2. Clients that cannot hold a websocket fall back to polling
`releases/desired.json` on their existing interval. This keeps the cacheability and
resumability properties that the whole transport was measured for.

### D2. Watcher is a poller over Registry v2, not an event consumer
`edgelab watch-registry` polls `GET /v2/<repo>/tags/list` (paginated via `n`/`last`) and
`GET /v2/<repo>/manifests/<tag>` with `Accept: application/vnd.docker.distribution.manifest.v2+json,
application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json,
application/vnd.oci.image.index.v1+json` reading `Docker-Content-Digest`. Token auth is
the standard two-step dance: ping `/v2/`, accept `WWW-Authenticate: Bearer
realm="…",service="…",scope="…"`, GET the realm with `service`+`scope`, cache the token
per scope until expiry. stdlib (`net/http`, `crypto/...`, encoding/json) covers this;
if the live variations prove hairy we take go-containerregistry and justify it in
PRODUCT_RESULT.md. Per-repo config: `repos:` entries with `allow` regex (required match
on tag), `ignore` regex (wins over allow), poll interval, and credential ref. State
(`repo/tag -> digest`) persists as JSON in the state dir written atomically; a digest
change on an allowed, non-ignored tag emits a publish request `repo:tag@digest`. The
publish trigger shells out to the existing pipeline conceptually: fetch manifest +
config + layers via the same v2 client into a docker-archive v2-style tar, then call
`lab.Publish` (chunk+sign) and `promote` to the desired channel. The registry-side
fetcher is deliberately small and testable against an httptest registry.

### D3. Notifications are an interface with two transports
```go
type Notifier interface {
    Name() string
    Send(ctx context.Context, ev Event) error
}
type Event struct {
    Kind      string    // release-published|delivery-staged|delivery-loaded|delivery-failed|watcher-error
    Release   string
    Repo      string
    Tag       string
    Digest    string
    Device    string
    Detail    string
    At        time.Time
}
```
Telegram: Bot API `sendMessage` (`https://api.telegram.org/bot<token>/sendMessage`,
`chat_id`, text with `MarkdownV2`-safe plain text; we send plain text to avoid escaping
bugs). Slack: incoming-webhook JSON `{"text": ...}`. Both behind retry with exponential
backoff + jitter, honoring Retry-After for 429. Config lives in sender and client
configs: `notifiers: [{kind: telegram, token_env: EDGELAB_TG_TOKEN, chat_id: ...},
{kind: slack, webhook_env: EDGELAB_SLACK_URL}]`. Secrets come from the environment,
never from the config file. Events are fanned out to all configured notifiers; a failing
notifier logs and is retried, it never blocks delivery. Unit tests use httptest servers
asserting path, body shape, retry-on-500-then-success, backoff growth and 429
Retry-After handling.

### D4. Hub multiplex and cache
The v2 FaultServer becomes the object engine behind a hub wrapper. Chunk GETs go
through `singleflight.Group` keyed by object path so N simultaneous misses produce one
disk read. Hot encoded chunks live in a bounded LRU (byte-budgeted, default 128 MiB,
configurable). Per-client identity comes from the `X-Edgelab-Device` header (or
query param) and is tracked as served bytes/requests/last-seen, exported via admin
socket and a Prometheus `/metrics` text block. The load test drives N=50 concurrent
httptest clients through a hub over a deliberately slow reader (wrapped file reads with
sleep) and asserts unique disk opens per object stay at 1 while every client still
verifies its bytes end to end.

### D5. Admin unix socket + edgelab-tui
`edgelab serve --admin-socket /run/edgelab/admin.sock` exposes newline-delimited JSON
request/response over a unix socket: `{"cmd":"stats"}`, `{"cmd":"clients"}`,
`{"cmd":"receipts","limit":N}`, `{"cmd":"events","limit":N}`, `{"cmd":"watch-status"}`.
Responses are one JSON document per request. Permissions 0660, created fresh at startup
(stale socket removed), removed on shutdown. The daemon keeps an in-process persistent
event log (JSONL, fsynced append, size-capped with rotation) — this replaces the
sender.py collector's observability function inside the Go daemon; sender.py remains as
historical evidence. `edgelab-tui` (separate binary, Bubble Tea) connects to the socket
path, polls at an interval, and renders hub stats, per-client table, recent receipts and
watcher status; a `--json` one-shot mode and `--csv` table export live in the same
binary so operators without a TTY still get Go-native exports.

### D6. Packaging
Multi-stage Dockerfile: build stage uses `golang:1.25` with `GOTOOLCHAIN=local`
semantics and builds both binaries statically (CGO_ENABLED=0); runtime stage is
`gcr.io/distroless/static` (or alpine if provenance is easier to reason about — choose
at implementation, record choice). The image runs `edgelab serve` as non-root with
volumes `/var/lib/edgelab/origin`, `/var/lib/edgelab/state`, and admin socket under
`/run/edgelab`. Helm chart `deploy/helm/edgelab-hub`: values for image, persistence
(PVC for origin+state), adminSocket path, notifier secrets (existing-secret reference or
literal env), resources, service (HTTP), and nodeSelector stubs. `helm lint` +
`helm template` smoke is the required gate; no cluster claims. Docker smoke runs on
OrbStack (local unix endpoint only) exercising publish→serve→sync inside containers or
container+host mix as feasible.

### D7. Client configuration and action modes
`edgelab client --config client.yaml` wraps the v2 `lab.Sync` agent:
```yaml
origin: http://hub:8080
manifest: http://hub:8080/releases/desired.json
public_key: /etc/edgelab/publisher.pub
state_dir: /var/lib/edgelab
cache_dir: /var/cache/edgelab        # optional; omit = ephemeral temp staging
allow: ["^prod/.*"]                   # release/repo allowlist
ignore: [".*-canary$"]
action: none                          # none (default) | load | restart
poll: 30s
notifiers: [...]                      # optional, same shape as sender
```
`none` performs the complete sync and verification path and reports what WOULD happen
(no docker load). `load` adds `--docker-load` semantics (explicit import after full
verification, image-ID checks). `restart` additionally maps the new image ID to running
containers (`docker ps --filter label=...` / image ID match) and recreates them via
`docker stop`+`rm`+`run` of the recorded config — implemented with the docker CLI
binary present on the client host (same trust boundary as v2's `docker image load`;
local unix socket only, `DOCKER_HOST` must be a local unix endpoint). Release-name
allowlist/ignorelist gates which `desired.json` sequences are eligible before any
download begins. The client binary is the same `edgelab` binary (portable single
binary), cross-compiled `CGO_ENABLED=0` for linux/amd64+arm64 and darwin/arm64.

### D8. End-to-end proof shape
One measured run on the host: fake registry (httptest, seeded with an image manifest),
watcher triggers publish, hub announces over websocket, N simulated spokes converge,
one real spoke performs a true `docker image load` into OrbStack with image-ID proof.
Timings captured publish→announce→spoke-sync-start→spoke-verified per client; capture
notification bodies at test endpoints. Evidence to `evidence/v4-productization/` with
PRODUCT_RESULT.md at workspace root containing the PASS/FAIL/NOT RUN gate table.

## Risks / Trade-offs
- [Bubble Tea dependency] → one TUI lib is the deliberate exception the brief allows;
  contained to `edgelab-tui` main + admin client types.
- [gorilla/websocket] → push-only usage is a tiny surface (announce messages only);
  poll fallback covers clients where the socket cannot be held.
- [Watcher publish pulls through the same registry client] → simpler than invoking a
  second toolchain, but the registry→archive exporter must be fuzzed against manifest
  lists; we scope v3 to single-platform manifests and record manifest-list handling as
  NOT RUN/limited if time-boxed.
- [In-process event log vs SQLite] → JSONL with rotation is dependency-free and
  sufficient for the TUI; SQLite in-process (mattn) would add cgo or modernc weight for
  little gain at this scale.

## Migration Plan
New code lands in new packages (`internal/registry`, `internal/notify`, `internal/hub`,
`internal/push`, `internal/admin`, `internal/client`, `cmd/edgelab-tui`) with the v2
`internal/lab` package imported, not modified, except narrowly justified additive
changes (e.g. exporting a stats hook from FaultServer for the hub wrapper). Existing
specs and commands keep their behavior; `make check` must stay green at every commit.

## Open Questions
None — the brief records the contested decisions (websocket scope, TUI form factor,
dependency budget) as made.
