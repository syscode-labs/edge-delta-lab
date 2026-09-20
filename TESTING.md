# Testing, evidence and limits

This is the current human-readable home for **what ran, what the numbers mean, and what remains unproved**. Evidence under `evidence/` and OpenSpec task histories are dated records, not claims about today's source or release. Their original version labels and checksums are retained. A configured CI workflow or a command below is not proof of execution.

Edge Delta remains experimental. Docker **loaded** means verified import, not running, healthy or still present in Docker. The publisher signs an exact archive; the receiver checks signature, sequence, sizes, encoded/raw chunk hashes and whole-archive identity before optional import. Never weaken those checks to make a test pass.

## Retained proof

| Scope | What the recorded execution establishes | Evidence |
|---|---|---|
| Synthetic transport, Linux amd64 | Real HTTP, forced process kill/restart, insertion reuse, skipped versions, corrupted-cache repair and offline reconstruction. Not runnable-image proof. | [Results](evidence/results.json), [events](evidence/demo-logs/), [source identity](evidence/TESTED_SOURCE_SHA256SUMS) |
| Real Docker, local OrbStack Linux VM | Three real images transferred with injected faults, imported and run offline. Five durable chunks survived SIGKILL; none re-requested. One local daemon, not remote-site proof. | [Results](evidence/revision-3/results.json), [validation status](evidence/revision-3/validation-status.json) |
| Registry watcher / hub / spokes | Registry fixture → publication → WebSocket hint → three staging clients plus one real Docker-load client. Notification capture was mock-only. | [Timing and retained state](evidence/v4-productization/v3-e2e/) |
| Long-lived Linux services | Ten-minute idle window, shaped delivery and bounded single-client HTTP-503 recovery. See measurements below. | [Independent verifier result](evidence/v4-productization/v31-linux/verification.json), [idle](evidence/v4-productization/v31-linux/idle.json), [link](evidence/v4-productization/v31-linux/link.json), [outage](evidence/v4-productization/v31-linux/soak/soak.json) |
| Cross-host Linux/Tailscale + Grafana | Standalone Linux amd64 hub → OrbStack Linux client, client ingress capped at 5 Mbit/s; two successful syncs, zero integrity failures. Dashboard queries retained the cold/update sequence. Synthetic archives were staged; this was not Docker activation. | [Access record](evidence/v4-productization/v31-access/), [cold proof](evidence/v4-productization/v31-grafana/cold-proof.json), [range](evidence/v4-productization/v31-grafana/cold-delta-range.json), [final queries](evidence/v4-productization/v31-grafana/final-query-results.json) |
| Helm lifecycle | Disposable single-node Kind/local-path install, upgrade, restart, rollback and uninstall; public-key-only client staged a signed synthetic archive. Not CSI portability, multi-node durability or Docker activation. | [Signed run](evidence/helm-kind-lifecycle/signed-run-3/result.json), [validation](evidence/helm-kind-lifecycle/validation.json) |
| Packaged Linux installation | Authenticated Distribution registry → bundled publisher/hub → trusted nginx HTTPS → separate systemd receiver and independent Docker store; two versions ran offline; service lifecycle and retained state checked. | [Result](evidence/intended-install/packaged-linux/result.json), [terminal completion](evidence/intended-install/packaged-linux/transcript.txt), [historical reproduction contract](evidence/intended-install/packaged-linux/README.md) |

The packaged proof ran source `3939fac3d24b3b275401d386fba1985a0e4df02d` with a locally built **v0.1.0** bundle. The two Ubuntu amd64 guests had separate Docker daemons/systemd managers but shared an OrbStack VM kernel. They used `vfs`/classic Docker storage after nested overlay failed. OrbStack disabled several systemd sandbox directives: this does **not** validate stock-unit hardening. The fixture registry used explicitly opted-in HTTP; only the hub-to-receiver leg used trusted HTTPS. HTTPS registry transport was not exercised by that proof.

The historical [closeout](evidence/intended-install/packaged-linux/VALIDATION.md) records serial Go/Python/race checks, bundle checks and official OpenSpec CLI 1.6.0 strict validation (3/3). This supersedes earlier “official CLI NOT RUN” notes for that source, not for arbitrary later changes. Source checksum lists are snapshots; documentation edits naturally change them.

### New usability and mTLS acceptance

**Do not treat the historical installer proof as execution of the v0.1.1 env/Make wrappers or optional Caddy mTLS path.** The separate [bounded installer/Caddy run](evidence/v0.1.1-install/README.md) records real cross-builds/checksums, extracted Linux arm64 keygen/installer-help/Make dispatch and OpenSSL enrollment without Go/source or a Docker socket in the test container. Installer contracts passed with **mocked systemd calls**. The [Caddy boundary result](evidence/v0.1.1-install/mtls-result.json) records an allowed client, absent/wrong-CA rejection, restart, removal preserving the loopback backend, independent proxy replacement and recreation. Its backend was a disposable HTTP test server, not the artifact hub.

That bounded run predates the integrated Go API changes and does **not** prove live v0.1.1 systemd installation/reboot or two-guest registry-to-Docker cold/update delivery through mTLS. Those remain **NOT RUN** here, as do remote DNS/firewall access and production certificate rotation/revocation. Rebuild and rerun acceptance for the integrated candidate; a merged harness is not an execution receipt. Public release URLs in the README are the v0.1.1 delivery target; a URL or successful local package build alone does not prove publication.

## Measurements and their meaning

### Small fixtures, not production-image predictions

| Scenario | Chunk response-body bytes | Metadata body bytes | Elapsed |
|---|---:|---:|---:|
| Synthetic cold download, killed and resumed | 17,072,868 | 53,581 | 30.286 s |
| Second synthetic image with shared base | 6,819,307 | 54,877 | 11.396 s |
| Small edit in the large synthetic layer | 249,778 | 53,581 | 0.572 s |
| Unchanged repeat | 0 | 0 | 0.053 s |
| Insertion / resynchronization | 253,874 | 53,581 | 0.574 s |
| Skip v1 directly to v3 | 262,066 | 53,581 | 0.600 s |
| Repair one corrupt cached chunk | 76,731 | 0 | 0.208 s |

Source: [retained synthetic results](evidence/results.json). Fixtures used 16 MiB per image, a 32 KiB edit and a 5,000-kbit/s response-body cap. Nine verified chunks survived the kill without re-request; offline assembly made zero HTTP requests. The base/app split is approximately 60%/40%, with deterministic hard-to-compress data—not zeros.

The packaged two-daemon proof used smaller real images:

| First loaded completion | v1 | v2 |
|---|---:|---:|
| Archive bytes | 3,653,120 | 3,653,120 |
| Downloaded chunks | 43 | 2 |
| Reused chunks | 0 | 41 |
| Chunk body bytes | 3,635,917 | 300,931 |
| Integrity failures / retries | 0 / 0 | 0 / 0 |

A receiver restart retained zero new chunk requests; other HTTPS requests still occurred. Neither table is a matched ordinary-registry-pull comparison.

### Daemon, link and dashboard measurements

- **Idle:** 600.49 seconds with three push clients and one 30-second polling client. All had zero response-body bytes; the polling client made 19 requests. Bootstrap was excluded. This is not zero wire traffic.
- **Kernel-shaped local delivery:** `tc netem rate 5mbit delay 100ms` affected both directions, approximately 200 ms RTT. Cold/shared-base/edit chunk bytes were 16,791,337 / 6,729,195 / 249,778; times were 48.44 / 20.11 / 1.86 seconds. Zero integrity failures. The cold body throughput did not saturate the cap. Timing is not directly comparable to the different application-fault model above.
- **Outages:** one client, two 30-second HTTP-503 windows, 206.57-second completion, 39 retry events, zero re-downloads of the 3 and 12 committed chunks checked at outage boundaries. This is not packet loss or fleet recovery. The corrected harness maps raw cache identities to exact encoded request paths; an earlier raw/encoded-hash comparison was rejected.
- **Grafana:** cold 16,791,337 bytes / 202 verified chunks; update 249,778 bytes / 3 verified chunks with 202 reused. Integrity failures stayed zero, successful syncs reached two, series count stayed 38. All 19 retained target/series queries succeeded, with two post-completion scrapes. A [historical screenshot](docs/GRAFANA.md#latest-supplied-screenshot-and-measured-context) is illustrative, not current service availability or authenticated automated rendering proof.

### Counters are not wire measurements

| Reading | Meaning / caveat |
|---|---|
| Origin `chunk_response_body_bytes` | Complete and partial chunk bodies accepted by the origin socket writer; not proof the client consumed them before a kill. |
| `metadata_response_body_bytes` | Manifest bodies. HTTP 304 removes the body, not request/header traffic. |
| Receiver counters | Bodies read, compressed bytes committed, retry overhead, integrity failures and raw-content reuse. A resumed invocation is not the killed process's total. |
| `summary.json`, last-sync metrics | Latest successful reconciliation, not immutable release history. No-op checks overwrite original transfer values; capture the first completion for each release. |
| Hub served bytes / lab receipts | Sending versus reported staging/import. Unauthenticated receipts are not trusted fleet evidence. |

Body bytes exclude HTTP headers, IP/TCP/TLS framing, acknowledgements, lower-level retransmissions and VPN overhead. The ordinary-layer baseline is **locally computed gzip size of missing fixture layers**, not a measured registry pull. It already credits a cached shared base; do not count ordinary layer reuse as an extra delta benefit. Last-run gauges are not lifetime counters; counters can reset at restart. Files/cache are the durable progress record, not unsynced event logs.

## Limits and missing evidence

| Boundary | Current limitation |
|---|---|
| Production-scale performance | Representative 1–2 GiB images and measured ordinary registry pulls **NOT RUN**. Small fixtures do not predict your savings. |
| Architecture | `linux/arm64` extracted-bundle keygen/enrollment ran in the bounded installer smoke test; full arm64 receiver delivery/systemd operation remains **NOT RUN**. macOS coordinated Linux tests; it is not Linux service proof. |
| Image formats | Registry tags must resolve directly to a single-platform image manifest. Multi-platform indexes/manifest lists are unsupported. Original registry digest associations and all OCI referrer/signature artifacts are not preserved by archive conversion. |
| Security | No core TLS, device enrollment, managed key/certificate rotation, TUF-style expiry/delegation/freeze protection or hardware monotonic counter. Optional external mTLS authenticates transport, not fleet policy or release freshness. |
| Storage | Unbounded chunk/archive retention; no quota or GC. Reserve space for raw cache, complete archive and Docker's extra storage. Each reconciliation rehashes referenced content. |
| Activation | Loading does not start/replace containers or health-check applications. The limited `conf restart` action is not a rollout controller and does not preserve arbitrary runtime settings. Image rollback is not data rollback. |
| Durability | Power loss, physical hardware, stock systemd hardening and independent-kernel packaged execution remain unproved. SIGKILL cannot establish that storage honors flushes. |
| Networks/fleet | Matched direct/Tailscale overhead, long multi-client packet-loss soak and simultaneous outage recovery **NOT RUN**. The bounded Tailscale result did not establish direct versus DERP. |
| Other integrations | Live Harbor-specific acceptance and live Telegram/Slack notification delivery are not established by registry/mock endpoint tests. |
| Tool/render evidence | Legacy Excalidraw browser import and authenticated automated Grafana capture remain **NOT RUN**. The new canonical diagram is editable HTML/SVG, not an Excalidraw runtime test. |

This independent Gear-style chunker is not desync/casync compatible or a full FastCDC implementation. Upstream [sources](docs/SOURCES.md) explain concepts; they do not certify this code. Evaluate maintained alternatives before adopting a custom fleet updater.

## Documentation-only validation

For this consolidation, the canonical diagram passed the installed diagram-design self-check and the repository's export/accessibility/geometry checks. Deliberately malformed connector, label-gap and accessible-ID cases were rejected. The actual browser render was visually inspected both as inline HTML (Chakra Petch / IBM Plex Mono loaded) and as a README-style SVG image with fallback fonts; no text clipping or hidden paths was observed. Browser text bounds also stayed inside the frame and nodes.

Local OpenSpec structural checks passed (42 requirements and three preserved historical editable scenes). The retained Linux-evidence verifier passed; it does not rerun Linux services. The original documentation pass checked Markdown local links/anchors and shell-fence syntax before sibling-workstream integration. `MTLS.md` and `GO_EMBEDDING.md` are now integrated; package contracts check bundled documentation link targets. These checks do **not** exercise live services or release download URLs.

## Run checks yourself

These are **reproduction commands, not fresh results**. Use one isolated checkout and run suites serially: overlapping suites previously caused a sender-test harness failure, while isolated serial runs passed. Go must match `go.mod`; Python 3.10+, make and Helm 3 are needed for full packaging/contracts. Docker/Kind tests need an explicitly disposable local daemon; never substitute a production daemon.

```sh
make -j1 check
make openspec-check
python3 scripts/watch_smoke.py
make build
python3 scripts/demo.py --work work/transport-fresh --size-mib 16 --rate-kbit 5000
make docker-smoke
make docker-demo WORK=work/docker-fresh SIZE_MIB=16 RATE_KBIT=5000
make install-contract
```

Choose fresh work paths. `make check` covers Go vet/tests/race and Python tests, not physical durability. The synthetic demo is transport-only; the Docker demo explicitly runs images offline. Inspect each generated `results.json`, logs and `RESULTS.md`, not just process exit. Sender-side demo visibility uses `make sender-status WORK=work/docker-fresh`; it does not scrape receiver files.

Additional checks:

```sh
# Requires installed official OpenSpec CLI; local structural check is separate.
openspec validate --all --strict --no-interactive
# Requires Docker, Kind, kubectl, Helm and matching Go/GOROOT.
python3 scripts/helm_kind_lifecycle.py --evidence work/helm-kind-fresh
# Inspect the historical Linux evidence with its independent verifier.
python3 scripts/verify_sustainability.py
# Local artifacts only: no upload, push or tag.
make release VERSION=v0.1.1
(cd dist && shasum -a 256 -c SHA256SUMS)
# Canonical diagram export consistency and deterministic geometry checks.
python3 scripts/diagrams.py --check
```

The release workflow may publish archive and container assets in independent jobs; a local build is not proof of either. Identical archive bytes need identical source/dependencies/toolchain and `SOURCE_DATE_EPOCH`. Linux bundles include the installer; Darwin binaries do not provide Linux systemd setup.

### Installer and mTLS checks

Run these from a **source checkout**, after building/checksumming the local candidate
as above. Choose amd64 instead of arm64 when that matches the disposable daemon.

```sh
make install-contract
python3 scripts/package_smoke.py --archive dist/edgelab-v0.1.1-linux-arm64.tar.gz
python3 scripts/mtls_smoke.py --work /tmp/edgelab-mtls-proof-fresh
```

The package smoke checks native keygen, installer help, Make dispatch and real
enrollment without Go/source or a Docker socket in its test container. The TLS
smoke uses owned disposable containers to test Caddy authentication, restart,
removal, replacement and recreation on the Linux Docker host network, not a
remote Internet path. It removes owned containers but retains test-only private
credentials under its fresh work directory: do not distribute or commit them.
Neither smoke proves systemd operation or full registry-to-receiver delivery.
Linux bundles carry documentation, diagram assets and embedding examples for
reference; source build/test/reproduction commands (including the example module's
local replacement of the main module) require a checkout. Links to omitted source
and historical evidence become explicit versioned web links in bundled Markdown.

### Fresh installation acceptance recipe (planned)

1. Package and checksum the candidate v0.1.1 source. Record the commit/toolchain and exact extracted bundle members. Use two disposable Linux hosts with independent Docker stores; record kernel, effective unit and storage driver.
2. Follow only [README](README.md) env/Make installation commands. Confirm publisher-only private signing key/registry credentials and receiver-only public signing trust. Keep credentials out of logs.
3. Publish two immutable, single-platform real images. Require signed archive/image identity checks, first loaded summaries and actual offline payload execution using `docker run --pull never --network none IMAGE_ID`.
4. Preserve first cold/update measurements and compare them from the same cache. Restart both sides; verify keys/sequence/cache continuity and no new chunk requests for unchanged content. Run status/stop/start/uninstall and assert retained data.
5. Separately test [mTLS](MTLS.md): unauthenticated request rejected, provisioned client succeeds, private CA/key material remains private, proxy stop/removal does not mutate publisher or hub state. TLS failure must never become an insecure fallback.
6. Remove only owned resources, verify cleanup and retain sanitized command results with clear PASS/FAIL/NOT RUN labels. This recipe is not a receipt that those steps ran.

### Measure a real site safely

Use [HTTP fault controls](examples/flaky.json) only on isolated tests. `rate_kbit` is aggregate body pacing; `latency_ms`, `fail_first`, `drop_every`, `drop_after_bytes`, `corrupt_first`, `stall_first`/`stall_ms`, `offline` and `offline_for_ms` operate at HTTP level, not packet loss. Atomic replacement works natively; a single-file Docker bind mount can retain the old inode.

Compare actual ordinary pulls and delta transfers from equivalent initial caches. Record CPU, elapsed time, disk high-water mark, flash writes and interface bytes, alongside body counters. Vary chunk size/concurrency and test expired credentials, full disks and long outages. Only authorized disposable namespaces/hosts should receive kernel traffic shaping; remove it afterward. Test actual abrupt power-off separately. Never change unrelated routes, firewalls, cloud accounts or Tailscale ACLs to make a test pass.

See [operations](deploy/compose/README.md), [architecture](docs/ARCHITECTURE.md) and the [evidence redaction policy](evidence/REDACTION.md) before collecting or sharing results.
