# v3.1 — Linux-daemon sustainability

## Verdict

**Local Linux-daemon measurements, the standalone OCI Linux proof, and the live Grafana cold-to-delta proof passed.**
The product remains OS-agnostic and Linux-first. macOS only coordinated these measurements.
Hub and clients ran matching static Linux amd64 binaries as continuously running daemon
processes inside an isolated Alpine container on the **local OrbStack Linux VM**. This is
not standalone-host production-readiness proof.

| Acceptance item | Result | Evidence / limitation |
|---|---|---|
| Static Linux amd64 and arm64 artifacts | PASS — build/inspection | Both static ELF; hashes retained. Only amd64 executed. |
| Ten-minute idle, hub + three push clients + one poll client | PASS — local Linux VM | 600.49 seconds; zero response-body bytes per device. |
| Kernel 5 Mbit/s + 100 ms shaping, three releases | PASS — local Linux VM | Real `tc netem`; byte deltas within 2% of baseline; zero integrity failures. |
| Bounded outage recovery without committed-chunk re-download | PASS — scoped local test | Two 30-second HTTP-503 periods; completion in 206.57 seconds. |
| Standalone Linux host | PASS — remote-linux-host | `remote-linux-host`, Linux x86_64, about 1 GiB RAM and 48 GiB disk; temporary user-owned processes only. |
| Actual Linux Tailscale delivery | PASS — bounded cross-host proof | Hub bound to remote-linux-host's Tailscale address; OrbStack Linux client completed the shaped cold and delta transfers. Direct-versus-DERP path and a matched overhead percentage remain NOT RUN. |
| arm64 runtime execution | **NOT RUN** | Build and ELF inspection only. |
| Multi-client outage herd / long-duration packet-loss soak | **NOT RUN** | Outage test has one client; HTTP 503 is not packet blackholing. |
| Low-cardinality exporter focused verification | PASS | `edgelab-exporter` built and served loopback Prometheus text from the Unix admin socket; bounded source failure behavior implemented. |
| Grafana cold→delta acceptance | PASS | Grafana Cloud retained cold samples of 16,791,337 bytes / 202 verified chunks and delta samples of 249,778 bytes / 3 verified chunks with 202 reused; integrity failures stayed 0, successful syncs reached 2, and series stayed at 38. All 19 dashboard target/series queries returned HTTP 200 + Prometheus success; two post-completion scrapes retained. |
| remote-linux-host ↔ OrbStack Linux final proof | PASS | OCI hub/exporter and OrbStack Linux client/exporter ran as daemons; client ingress used a 5 Mbit/s Linux qdisc; Grafana retained the cold and changed-image delta results with zero integrity failures. |

Evidence: [access probes](evidence/v4-productization/v31-access/README.md),
[Linux results and reproduction](evidence/v4-productization/v31-linux/README.md),
[machine-checked summary](evidence/v4-productization/v31-linux/verification.json).
Product telemetry/exporter source and this acceptance evidence are committed together.

## v3.1 telemetry and dashboard

The versioned dashboard source is `deploy/helm/edgelab-hub/dashboards/edgelab-delivery-cache.json`.
Public evidence is available in the [Grafana guide and historical screenshot](docs/GRAFANA.md) and [retained dashboard queries](evidence/v4-productization/v31-grafana/README.md); the private hosted dashboard is not a public evidence destination.
It was read back after import (version 2, 10 panels) and every panel target plus the fixed
series-budget query was executed through the real `grafanacloud-prom` datasource. All 19
queries returned HTTP 200 with Prometheus `status=success`. The retained range evidence shows
cold `16,791,337` bytes / `202` verified chunks and delta `249,778` bytes / `3` verified chunks
with `202` reused chunks. Integrity failures remained a zero-valued series, successful syncs
reached `2`, and the fixed series budget remained `38` (≤40). Two post-completion scrape
snapshots were retained. Sanitized metadata and query results are retained in
`evidence/v4-productization/v31-grafana/`.

The exporter uses a 2-second Unix-socket deadline, a 1 MiB response bound, fixed
`job`/`instance`/`role` labels, and no Go default collectors. Only an explicit
`edgelab_*|up` allowlist is present in the example Alloy configuration. A historical screenshot
is now included in the [Grafana guide](docs/GRAFANA.md). **Authenticated automated dashboard
capture/render verification** remains **NOT RUN**; successful panel queries do not establish it.

## External-host proof and isolation

Publication redaction: host labels, endpoint addresses, personal paths and account identity
are replaced throughout the retained evidence; measurements are unchanged. See the
[redaction and integrity note](evidence/REDACTION.md).

Access to `remote-linux-host` used an existing authorized private access path.
The host was confirmed as Linux x86_64 with about 1 GiB RAM,
48 GiB disk, Docker and Tailscale already running. The hub and exporter were copied into an
owned `/tmp/edgelab-v31-*` area and listened only on the host's Tailscale address
(`192.0.2.20` is a documentation-only replacement). The OrbStack Linux client reached that endpoint through the existing
Tailscale path while Linux IFB/TBF ingress shaping enforced 5 Mbit/s.

This proves the bounded cross-host Tailscale delivery path. It does not establish a matched
direct-path overhead percentage: the available path classification was not retained strongly
enough to call direct versus DERP. No OCI resources, firewall rules, routes, Tailscale
ACL/account settings or installed system services were changed. All owned remote processes
and files, local containers, forwarding processes and qdiscs were removed and cleanup was
verified.

Fallback kernel: `7.0.14-orbstack-00380-ga7e0a2dc9535`, x86_64. The disposable container had
273 GiB initially available, Python 3 and iproute2. It had NET_ADMIN only within its own
network namespace, no host networking, no Docker socket and no mounts. Generated keys,
fixtures and cache lived in an owned temporary directory and were removed. No credentials
are retained in the evidence.

## Idle traffic

All clients first converged on the initial release; that bootstrap is excluded from the
idle window. No new release was promoted during the window. Daemon liveness was sampled
every second, and a process/socket snapshot records the three persistent push connections.

| Device | Mode | Requests in 600.49 s | Chunk requests | Response-body bytes | Observed body KiB/device/hour, normalized |
|---|---|---:|---:|---:|---:|
| push-1 | Push, one-hour fallback poll | 0 | 0 | 0 | 0 |
| push-2 | Push, one-hour fallback poll | 0 | 0 | 0 | 0 |
| push-3 | Push, one-hour fallback poll | 0 | 0 | 0 | 0 |
| poll-1 | 30-second polling | 19 | 0 | 0 | 0 |

Gate: at most 2,048 body bytes/device/ten minutes. All four pass. The poll client remained
active and used the zero-body conditional path; this is not a pass from absent clients.
**Zero body bytes is not zero wire traffic**: HTTP headers, WebSocket keepalives, TCP/IP and
VPN packets are excluded. Hourly values are normalization of this window, not an hour-long run.

## Kernel-shaped delivery

One Linux hub and one continuously running Linux watch client transferred three promoted
releases without restarting the client. Fixtures are deterministic synthetic Docker archives;
this stage does not claim Docker import or application activation.

`tc netem rate 5mbit delay 100ms` shaped the container loopback queue. Both directions pass
through that queue, giving approximately **200 ms RTT**, not 100 ms RTT. The application's
rate limiter was disabled for this stage. Stats/control traffic shared the queue.

| Scenario | Chunk body bytes | Metadata body bytes | Elapsed | Prior chunk-byte baseline | Difference |
|---|---:|---:|---:|---:|---:|
| Cold A v1 | 16,791,337 | 53,581 | 48.44 s | 17,072,868 | −1.65% |
| Second image B v1, shared cache | 6,729,195 | 54,877 | 20.11 s | 6,819,307 | −1.32% |
| Small edit A v2 | 249,778 | 53,581 | 1.86 s | 249,778 | 0.00% |
| Unchanged, subsequent five seconds | 0 | 0 | 5 s observation | 0 | unchanged |

All staged archives passed the existing integrity checks; no retries or integrity failures
occurred. The qdisc recorded 24,558,682 bytes and 5,520 packets, with zero drops. That count
includes headers/control traffic and is not a VPN-overhead number. Observed cold body
throughput was **2.78 Mbit/s**: the cap was respected but not saturated. Per-chunk round trips
and two-worker concurrency matter at this delay. The prior 30.3-second cold baseline used a
different in-process delay/fault model; these timings are not an apples-to-apples regression
comparison. Its injected faults also account for additional baseline body bytes.

For planning only, one GiB at a perfectly utilized 5 Mbit/s takes **1,717.99 seconds
(28.63 minutes)** before headers, retransmissions, VPN or processing. This is a mathematical
lower bound, not a measured one-GiB completion time. Measured update payloads here were
6.729 MB for the second image and 0.250 MB for the small edit, plus metadata.

## Outage recovery

A fresh Linux hub and continuously running Linux watch client used the same kernel queue
plus a **1,000-kbit/s application pacer**, leaving enough outstanding work to exercise two
separate 30-second HTTP-503 periods. This deliberately tests unavailable-server recovery;
it is not a packet-loss, TCP blackhole or multi-hour soak.

- Both outages occurred before completion and increased observed HTTP-503 counters.
- At their boundaries, **3 and 12 committed cache chunks** were independently size/hash
  verified and mapped through the manifest to exact encoded object-request paths.
- Every mapped path's request count remained unchanged through final completion:
  **zero re-downloads of those already committed chunks**.
- **39 retry events** were observed. Highest logged attempt: **10**, below the configured
  12-attempt per-object limit. Maximum observed delay: **5,000 ms**, matching the backoff cap.
- Hub and client remained alive. Final state was `staged`, with zero integrity failures,
  after **206.57 seconds**. Retry events and full client logs are retained.

A review caught a false-pass risk in the first harness: raw cache hashes were being compared
with encoded HTTP object hashes. That soak result was not accepted. The corrected soak was
executed separately and the exact-path comparisons were independently rechecked by
`scripts/verify_sustainability.py`. This was a measurement-harness correction, not a change
to product durability or integrity checks.

## Conservative settings for a 5 Mbit/s site

- Use push announcements with a long fallback poll (one hour was measured), or 30-second
  polling if push is unavailable. Avoid one-second production polling; that interval was
  used only to speed controlled release promotion in the link test.
- Start with two workers, 12 per-object attempts, 200 ms initial backoff and a five-second
  cap. The soak measured these settings; it did not validate a multi-client herd.
- Keep the aggregate hub body cap at or below the link budget. Reserve capacity for other
  traffic and VPN overhead rather than assuming a 5,000-kbit body cap fits a 5,000-kbit wire
  link. An initial 4,500-kbit body cap is a conservative tuning suggestion, **not a measured
  Tailscale result**; adjust after real endpoint measurements.
- Use a request timeout long enough for the largest chunk on the slowest supported link.
  The soak used ten seconds; the normal 30-second default gives more margin. Do not make
  attempts unlimited merely to hide persistent failures. Watch continues later reconciliation
  cycles after a failed cycle, so per-object attempt bounds are not a lifetime retry limit.

## Verification and cleanup

Fresh serial `make check` passed: vet, ordinary Go tests, 35 Python tests and race-enabled
Go tests. Those are workstation regression checks, not standalone Linux runtime proof.
The historical overlapping background-suite failure in `scripts/sender_test.py` was a
**test-harness execution artifact**. Serial isolated foreground `make test`/`make check`
passed; these execution labels describe the suite runner, never hub/client behavior.

The retained evidence verifier passes. Both runs reaped child daemons, removed generated
keys/fixtures/cache and removed netem. Final container state was captured, then the owned
container was removed. No temporary Linux daemon, test filesystem or qdisc is retained.

**Remaining unmeasured items:** arm64 runtime execution, a matched direct-versus-Tailscale
overhead percentage, and a longer multi-client packet-loss soak. They do not invalidate the
completed bounded proof: standalone OCI Linux served both releases, the shaped OrbStack Linux
client downloaded the full image and then only the small changed delta, and Grafana retained
the expected byte, chunk, reuse and integrity results.
