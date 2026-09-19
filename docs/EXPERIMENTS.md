# What the tests prove

Use this page to judge which results apply to your devices, how to read the measurements, and what still needs testing.

The recorded tests show that a device can reuse downloaded content, fetch only the missing pieces of a changed image, and recover after its download process is killed without downloading already-saved pieces again. Separate tests loaded real Docker images and ran them without the server or network. A bounded cross-host test also delivered a full image and a small update over Tailscale using continuously running Linux services.

These are working demonstrations, **not production-readiness or fleet-scale performance measurements**. The retained image tests use small, controlled test images (fixtures). They do not predict savings for your images.

A **chunk** is a piece of an archive that can be downloaded and verified independently. A **manifest** is the signed description of the release and its chunks. A **cache** stores downloaded content for reuse. A **digest** is a hash, or fingerprint calculated from bytes, used to identify and verify content. Content-defined chunking chooses boundaries from the data, rather than fixed positions, so unchanged content can still be reused after an insertion.

## Measured evidence

### Download less when content is already on the device

The [synthetic transfer results](../evidence/RESULTS.md) used 16 MiB per image, a 32 KiB edit and a 5,000-kbit/s response-body limit. The traffic was real HTTP on the same machine. It was not a registry pull or a Docker import test.

| What the device did | Chunk body bytes | Metadata body bytes | Seconds |
|---|---:|---:|---:|
| First download, process killed and resumed | 17,072,868 | 53,581 | 30.286 |
| Download a second image sharing the base | 6,819,307 | 54,877 | 11.396 |
| Download a small edit in a large layer | 249,778 | 53,581 | 0.572 |
| Check the unchanged release again | 0 | 0 | 0.053 |
| Update after bytes were inserted | 253,874 | 53,581 | 0.574 |
| Skip directly from v1 to v3 | 262,066 | 53,581 | 0.600 |
| Repair one corrupted cached chunk | 76,731 | 0 | 0.208 |

Nine verified chunks survived the process kill; none were requested again. Rebuilding the archive offline required zero HTTP requests. The skipped-version test used a copy of a device directory after v1, not arbitrarily preloaded chunks.

The fixtures have an approximately 60% common base layer and 40% application layer. Both contain deterministic, hard-to-compress data, not zeros. App A v2 changes bytes within the application layer; v3 also inserts bytes and shifts the rest. App B shares the base but has different application content.

### Load and run real Docker images

The [real Docker results](../evidence/revision-3/RESULTS.md) passed on OrbStack 29.4.0, using a linux/amd64 Docker daemon in a local Linux VM, coordinated from a macOS arm64 workstation. This was a same-daemon local integration test, not a standalone production deployment.

Three releases were built, transferred at 5,000 kbit/s with injected disconnects, HTTP 503 responses and corruption, then imported and checked by running containers. A mid-transfer process kill preserved **5 durable chunks, with 0 re-requested**. Old and new images ran with the origin stopped, container networking disabled and `docker run --pull never`. The separate Docker smoke test passed with distinct configuration and layer digests per build.

**Loading an image is not starting or health-checking your application.** The test explicitly ran containers to verify their payloads; a normal loaded acknowledgement reports import only.

<a id="linux-first-daemon-acceptance"></a>

### Keep Linux services running through idle time and outages

The [Linux measurements](../SUSTAINABILITY.md) distinguish local Linux-VM tests from the standalone-host proof.

| Test | Observed result | What it means for you |
|---|---|---|
| Idle hub and three push clients plus one polling client, local Linux VM | 600.49 seconds; zero response-body bytes per device | Unchanged releases did not download content. Headers and connection keepalives still use the network. |
| Three releases under Linux traffic shaping, local Linux VM | 5 Mbit/s plus 100 ms queue delay; byte totals within 2% of the prior baseline; zero integrity failures | Continuously running services delivered verified archives under this controlled delay. Both directions used the queue: approximately 200 ms round-trip time, not 100 ms. This stage did not import or activate Docker images. |
| One client, two server outages | Two 30-second HTTP-503 periods; completion in 206.57 seconds | Recovery worked without re-downloading the 3 and 12 committed chunks checked at the outage boundaries. This was not packet loss or a multi-client outage. |
| Standalone Linux amd64 hub to an OrbStack Linux client over Tailscale | 5 Mbit/s limit on traffic arriving at the client; two successful syncs; zero integrity failures | The cross-host delivery path worked for a bounded cold download and changed release. It does not measure VPN overhead or prove a direct rather than relayed path. |
| Live Grafana evidence for cold download and update | 16,791,337 bytes / 202 verified chunks, then 249,778 bytes / 3 verified chunks with 202 reused | Dashboard data retained the expected transfer reduction. Integrity failures stayed 0, successful syncs reached 2 and the number of distinct metric time series stayed at 38. All 19 dashboard target/series queries succeeded; two post-completion scrapes were retained. |

## Read the numbers correctly

The ordinary-layer comparison is the **locally computed gzip size of missing fixture layers**, not a measured Docker registry pull. It accounts for an already-cached shared base. Ordinary layer reuse is not counted as a benefit of reusing chunks within a layer.

| Metric or file | What it tells you | What it does not tell you |
|---|---|---|
| `chunk_response_body_bytes` | Complete and partial chunk bodies accepted by the origin's socket writer | Whether the device consumed every byte before a process kill |
| `metadata_response_body_bytes` | Manifest response bodies; unchanged conditional requests return HTTP 304 with no body | Total request or connection overhead |
| Agent counters | Bodies read, committed compressed bytes, retry overhead, integrity failures, request counts and reuse of uncompressed cached content | Server totals across a killed process and its replacement; a resumed summary covers only that invocation |
| `/metrics`, `/stats` | Origin Prometheus metrics and detailed request counts | Actual network-interface traffic |
| JSONL events, `state.json`, `summary.json`, `agent.prom` | Receiver events, atomically replaced state, successful-invocation summary and textfile-collector metrics | A complete crash history: logs and metric events are not synced after every event |

Body counters exclude HTTP headers, Ethernet/IP/TCP/TLS framing, acknowledgements and lower-level retransmissions. They also do not measure VPN overhead. Last-run metrics are gauges (values for that run), not lifetime counters. Files and verified cache contents are the durable progress record.

## Reproduce the checks

| Command | What it checks |
|---|---|
| `make check` | Go integration/unit tests, `go vet`, race detection and Python normalization tests |
| `python3 scripts/watch_smoke.py` | One real client process discovers two successive changes to the selected release from a real server |
| `make demo` | Independent processes and a real SIGKILL (forced process termination), including recovery and version skipping |
| `make docker-smoke` | Opt-in test requiring a real Docker daemon; not part of transfer-only evidence |
| `make docker-demo WORK=work/docker-validation-007 SIZE_MIB=16 RATE_KBIT=5000` | The real-Docker demonstration command used for the retained revision-3 evidence; choose a fresh work directory for a new run |

Automated cases cover insertion reuse, repeatable chunk boundaries, compressed and uncompressed corruption, version skipping, repeat checks, signature tampering before content requests, refusal of older or conflicting releases, truncated bodies, timeouts, HTTP-503 retries, cancellation recovery, shared bandwidth, disk-space checks, single-writer locking and offline assembly.

The [retained evidence](../evidence/) includes compiler, fixture, link, worker and fault settings in the reports, `results.json`, `checks.txt` and event logs. A configured CI job does not prove hosted CI ran. Foreground tests and a one-shot sync do not prove sustained service behavior.

### Test-suite execution anomaly

Test suites run at the same time in one checkout failed in `scripts/sender_test.py`. Serial, isolated foreground runs of `make test` and `make check` passed, including a fresh serial `make check` during v3.1 validation. Run suites one at a time in an isolated checkout. This was a shared test-runner problem, not evidence of a hub or client runtime failure.

An initial outage measurement compared uncompressed cache hashes with compressed download hashes and could have falsely reported no re-downloads. That result was rejected. The corrected test compared exact request paths and was independently checked by `scripts/verify_sustainability.py`; the accepted results above use that corrected measurement. Product integrity checks were not weakened.

## What comes from source, not measurement

The [upstream references](SOURCES.md) explain content-defined chunking, signing and Docker archive/loading behavior. They do not certify this implementation. This lab uses its own format, not desync's format, and is not a full FastCDC implementation. Its measured performance must not be presented as desync performance.

## Tests still NOT RUN

| Missing evidence | Boundary |
|---|---|
| Representative production 1–2 GiB images and actual ordinary registry pulls | **NOT RUN / not measured** in the retained results. Small fixtures and calculated transfer-time lower bounds are not substitutes. |
| `linux/arm64` runtime | **NOT RUN**; static build and executable-format inspection only |
| Matched direct-versus-Tailscale overhead comparison | **NOT RUN**; body counters cannot establish VPN/wire overhead |
| Long multi-client packet-loss soak | **NOT RUN**; one-client HTTP-503 recovery is not packet blackholing or simultaneous fleet recovery |
| Power-loss and hardware validation | **NOT RUN**; SIGKILL does not test a power cut or whether storage honors flushes |
| Official OpenSpec CLI validation | **NOT RUN**; CLI unavailable, local structural check only |
| Excalidraw browser import | **NOT RUN**; structural diagram checks do not prove editor import |
| Authenticated automated dashboard capture/render verification | **NOT RUN**; a [historical dashboard screenshot is included](GRAFANA.md#latest-supplied-screenshot-and-measured-context), alongside retained panel queries, but it does not establish automated authenticated capture |

## Test your own link safely

Copy [the fault-plan example](../examples/flaky.json) and adjust these fields:

| Field | Meaning |
|---|---|
| `rate_kbit` | One aggregate response-body limit in decimal kbit/s; 0 disables it |
| `latency_ms` | Delay before each response, not a packet-level round-trip model |
| `fail_first` | Return HTTP 503 for the initial content requests |
| `drop_every` | Truncate every Nth chunk request |
| `drop_after_bytes` | Send this body prefix before closing the socket |
| `corrupt_first` | Corrupt the first N chunk responses |
| `stall_first`, `stall_ms` | Delay selected chunk responses to exercise client deadlines |
| `offline` | Refuse content requests while true; control and metrics remain reachable |
| `offline_for_ms` | Refuse content requests during the initial server interval |

The plan is read again for each request. Atomic file replacement works in the native-file demo. A Docker single-file bind mount can keep pointing to the old file; edit it in place or recreate that origin container.

These controls simulate failures at the HTTP level. Packet loss/reordering, burst loss, TCP congestion, real reconnect timing, DNS outages, TLS renegotiation and radio behavior need a separate isolated VM, network namespace or hardware testbed. Do not impair the network interface of a remote production device.

For Linux acceptance tests, run the matching static binary and observe the services throughout idle and bad-link tests; building a binary is not runtime proof. Restrict kernel traffic shaping to an authorized test container or host and remove it afterward. macOS `pf`/dummynet is not the production acceptance path. Do not change unrelated host networking, cloud account/network/firewall configuration or Tailscale access rules to make a test pass.

Before deployment, test representative images and archive formats against actual ordinary pulls from the same starting cache. Measure CPU, elapsed time, peak disk use, flash writes and network-interface bytes; vary chunk sizes and concurrency. Test outages long enough to cross credential and request timeouts.

Use abrupt VM power-off or a controlled physical power interrupter for durability checks. Confirm storage honors flushes and test full disks during download and Docker unpacking. Verify that staging leaves the running application intact, then separately validate health-gated activation and rollback compatible with application data. See [production readiness](PRODUCTION_GAPS.md) for the remaining decisions.
