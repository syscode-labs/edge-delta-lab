# Design

## Context
The device runs Docker. Its network is slow and intermittent. Builds are large
but mostly unchanged. Whole-layer caching alone can still retransmit a large
modified layer. Observability must be operated from the sender, not by logging
into the customer's device.

## Goals / Non-Goals
Goals: prove the simple end-to-end mechanism, measure data, exercise actual retry
and restart behavior, and provide inspectable sender evidence. Non-goals:
production enrollment, remote application rollout, database rollback, automatic
GC, replacement of Docker, a new image registry protocol, and claimed power-cut
validation from a process-kill test.

## Architecture
`docker build/save -> normalize exact layer bytes -> CDC -> per-chunk gzip ->
signed recipe + origin -> unreliable HTTP -> durable edge cache -> exact archive
-> docker load -> docker run --pull never --network none`.

The sender collector polls only the origin's `/stats` and `/receipts`. It does not
read edge files, scrape an edge endpoint, or use SSH. The receiver may POST a
`staged` or `loaded` acknowledgement outward. Until then, delivery is unknown.

Native diagrams: `docs/diagrams/architecture.excalidraw`,
`chunk-delta.excalidraw`, and `retry-and-observability.excalidraw`.

## Decisions
1. Keep the self-contained CDC implementation for this demonstrator. It is not
   desync-compatible. Evaluate a maintained engine separately before production.
2. Normalize Docker-save archives by decompressing layer streams without extracting
   or re-tarring their filesystems. Verify each uncompressed DiffID. Unsupported
   encodings fail rather than weakening identity checks.
3. Default to one local Docker daemon with unique test tags. Remove source image
   references and assert the target ID is absent before transfer. Support two
   existing Docker contexts and record whether daemon IDs actually differ.
4. Prove the running payload, not just a version label: the `FROM scratch` probe
   computes SHA-256 over both the shared base file and the large changed file.
5. Measure transfer bytes at the origin. Compare with the computed gzip size of
   missing whole layers, explicitly not a measured ordinary registry pull.
6. Keep sender history in SQLite WAL. Detect origin counter epochs; never derive
   a negative rate or pretend reset counters are a new cumulative total.
7. Persist an outbound receipt before attempting POST. Keep it after failed POST,
   replay on a future online reconcile, and store it by content-derived ID at the
   sender. A duplicate POST cannot create a second receipt.
8. Keep `LOADED` separate from `RUNNING/HEALTHY`. The lab's runtime test records
   actual probe output separately; the collector never infers health from load.

## Interfaces and State
Origin GET `/stats`: process-scoped counters and origin start epoch.
Origin GET/POST `/receipts`: durable loaded/staged acknowledgements, only enabled
by `--receipts-dir`. Agent `--receipt-url` and `--device-id` opt into reporting.
`state/outbox/*.json` survives restarts. A successful POST removes and syncs the
outbox entry. A failure preserves it; image import does not roll back because an
acknowledgement failed. Receiver state remains the authority for local execution.

## Failure Semantics
Incomplete/corrupt object: reject and retry. SIGKILL: reuse committed cache.
Offline sender: collector records unreachable and preserves historical data.
Lost receipt: report UNKNOWN or last acknowledgement, never guess current state.
Duplicate receipt: idempotent persistence. Docker load failure: retain the verified
archive locally for another import. Disk errors stop the update without deleting
running/rollback content.

## Measurement Contract
Origin byte counters cover HTTP response bodies written by the server, including
retry waste. They exclude request headers, TLS/TCP/IP overhead, acknowledgement
POST bodies, and collector traffic. A socket write is not durable delivery.
Device-provided reuse counts and phases are acknowledgements, not independently
observed progress. The lab endpoint has no device authentication; do not treat it
as trustworthy across an untrusted network. The baseline is missing-layer gzip
bytes computed from the exported artifact, not packet-captured registry traffic.

## Validation Strategy
Run `make check`, `make openspec-check`, `make diagrams`, the measured transport
experiment, and `make docker-demo`. Inspect the generated JSON/Markdown report
and sender SQLite/CSV evidence. CI must retain the Docker report even on failure.
Real Docker execution remains pending until that command runs on a Docker host;
a workflow file alone is not evidence. Repeat 1-2 GiB tests on actual production
images and device storage before drawing production sizing conclusions.

## Risks / Trade-offs
The JSON chunk index is verbose. The cache, outbox and retained archives need
bounded GC in production. Every reconcile validates content and can be IO-heavy.
A process-level kill does not simulate power loss. The application-level fault
server is not a packet-loss emulator. Compression/metadata/build changes can
reduce reuse. Existing Docker content is not automatically a warm chunk cache.

## Rollout / Rollback
This repo does not replace running application containers. The test runs isolated
short-lived probes by image ID. Keep current and previous images available; a
production activation controller and data-migration policy are separate changes.
