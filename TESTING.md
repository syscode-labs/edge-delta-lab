# Testing, evidence and limits

This is the current human-readable home for **what ran, what the numbers mean, and what remains unproved**. Evidence under `evidence/` and OpenSpec task histories are dated records, not claims about today's source or release. Their original version labels and checksums are retained. A configured CI workflow or a command below is not proof of execution.

Two frozen evidence indexes retain relative links to guides removed in the documentation consolidation. Read those indexes in the [pre-consolidation packaged-install snapshot](https://github.com/syscode-labs/edge-delta-lab/blob/811b082eeaec9deeefce4bd85d942dff5763257e/evidence/intended-install/packaged-linux/README.md) and [revision-3 snapshot](https://github.com/syscode-labs/edge-delta-lab/blob/811b082eeaec9deeefce4bd85d942dff5763257e/evidence/revision-3/README.md), where the linked files and anchors remain available. Their bytes and checksum records were not rewritten to follow the current guide map.

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

**Fresh local packaged acceptance: PASS, 2026-09-20.** Attempt `work/usability-local-08` passed all 17 gates with `cleanup_errors: []` and `guests_retained: false`. The [result](evidence/usability-mtls-20260920/result.json) records the exact locally rebuilt v0.1.1 archive/binary hashes. Its source was integration commit `4a8c5cc60eeb00f73b512c7348f2ca853c8e1a50` plus the installer/harness repair identified by [source hashes](evidence/usability-mtls-20260920/source-sha256.json), not the unchanged commit alone. Documentation consolidation happened afterward; bundled documentation is a historical snapshot.

| Executed boundary | Observed result |
|---|---|
| Package-only setup | Linux amd64 release binary executed on both guests through Make/env setup; unprivileged hub workflow and explicit receiver sudo, no guest compiler/source checkout. |
| Trust boundaries | Anonymous registry access denied; signing key mounted only in publisher; remote raw HTTP inaccessible; enrolled mTLS client accepted, absent/wrong-CA client rejected. |
| Cold delivery | Signed config identity matched receiver Docker bytes; explicit offline execution returned `edge-delta-version-1`; 128 chunks downloaded. |
| Publisher restart and new tag | Distinct signed v2 config identity and offline payload `edge-delta-version-2`; 126 chunks reused, 2 downloaded. |
| Receiver restart | Fresh loaded summary reused all 128 chunks, downloaded none; origin chunk requests stayed at 2. |
| Runtime key lifecycle | Key mode `0600`, correct live DynamicUser ownership, private runtime directory; key absent after stop. |
| Proxy lifecycle | Removal closed TLS while preserving loopback hub; independent replacement accepted enrolled client and rejected anonymous access; packaged wrapper recreated. |
| Offline availability and uninstall | Both versions executed with `--pull never --network none` after hub shutdown. Services/binaries removed while signing trust, client state, origin data and TLS material were retained before teardown. |
| Cleanup | Both exact owned guests deleted successfully; no retained guests or cleanup errors. |

[Cold](evidence/usability-mtls-20260920/summary-1.json), [update](evidence/usability-mtls-20260920/summary-2.json) and [restart](evidence/usability-mtls-20260920/summary-2-restart.json) summaries preserve first-completion measurements. [Lifecycle probes](evidence/usability-mtls-20260920/lifecycle-probes.json) retain effective units and key metadata; [installed byte comparison](evidence/usability-mtls-20260920/installed-receiver-bytes.json) matched the receiver binary to the archive and installed manager to repaired source. Import did **not** activate containers: offline execution was an explicit harness action.

**Scope:** two Ubuntu 24.04 amd64 OrbStack LXC guests with independent classic `vfs` Docker stores, but a shared kernel—not independent physical hosts or a WAN. OrbStack globally clears `LoadCredential`; a disclosed receiver-only fixture drop-in restored the packaged declarations and added a nonsecret mode diagnostic. This is not untouched native-host sandbox or reboot proof. Only amd64 executed in this run; arm64 was archive/ELF inspection only. The packaged publisher writes the shared local hub filesystem; it has no outbound publisher-to-hub mTLS option, and that path was not exercised.

**Go embedding and HTTP mTLS:** [GO_EMBEDDING.md](GO_EMBEDDING.md) and [MTLS.md](MTLS.md) are integrated guides, not pending integrations. The fresh [make-check log](evidence/usability-mtls-20260920/make-check.txt) records Go vet, uncached Go tests and race tests (including `embedding` and `hubclient`), 82 Python tests and 3 Compose setup tests passing. Go integration covers the public publisher/receiver agents, registry publication, receiver-to-hub HTTP mTLS staging and receipt delivery. It does not establish a remote HTTP publisher upload, Docker activation or the separate example module's test execution. Keep that Go integration scope separate from packaged registry-to-Docker acceptance.

<a id="hosted-v011-acceptance-and-v012-delivery-status"></a>

#### Hosted v0.1.2 acceptance and complete release delivery

**Published v0.1.2 Linux amd64 acceptance: PASS, 2026-09-20.** [Release v0.1.2](https://github.com/syscode-labs/edge-delta-lab/releases/tag/v0.1.2) was built from `2d860aecf5f33c00d553a775b7f99d7e7ca7b16c`. Release workflow [35511807780](https://github.com/syscode-labs/edge-delta-lab/actions/runs/35511807780) concluded **success**, including all four jobs: `validate`, `github-release`, `containers (daemon, edgelab)` and `containers (client, edgelab-client)`. Both container build/push steps succeeded; that is publication evidence, not a runtime test of those GHCR images. Test workflows [35511696941](https://github.com/syscode-labs/edge-delta-lab/actions/runs/35511696941) and [35511807775](https://github.com/syscode-labs/edge-delta-lab/actions/runs/35511807775) also succeeded for that source. [Raw workflow records](evidence/usability-hosted-v0.1.2/README.md) preserve the job/step conclusions and URLs.

Anonymous [GHCR descriptor verification](evidence/usability-hosted-v0.1.2/public-oci.json) confirmed both `linux/amd64` and `linux/arm64` manifests and all four configuration revision/version labels against the release source. Index identities are `edgelab@sha256:a5001081c885de3237987822728b25af5bcf1abeabed20de8e97f93129f20cc0` and `edgelab-client@sha256:9b80443dee4a0458f7eda711d43c7af2493825eba8f6ef117d27e3cb23f6be17`. Manifest/config bytes matched their digests; this read-only check did not pull layers or execute arm64 containers.

Attempt `work/usability-hosted-v012-01` executed the publicly hosted Linux amd64 archive—not a local rebuild—and passed **all 17 gates**. The [result](evidence/usability-hosted-v0.1.2/result.json) records the v0.1.2 source URL, archive SHA-256 `04b48a478f67cfd489f3c870ade231a0cb3cebc14665029f438ecc6626b7a4a2` and executable SHA-256 `4807d57b2bb71e3b8b8e1bc23520bb8beb0b418f8eedc8550cb27b67f9fd6bc6`. [Anonymous download records](evidence/usability-hosted-v0.1.2/anonymous-assets.json) retain five public HTTP-200 assets and their hashes. All four archives/chart matched the downloaded [SHA256SUMS](evidence/usability-hosted-v0.1.2/SHA256SUMS); the checksum file's own hash is recorded separately in the download records. [Archive inspection](evidence/usability-hosted-v0.1.2/archive-inspection.json) records members, executable architecture and chart metadata; its `runtime_executed: false` describes inspection, not the later amd64 acceptance.

| Hosted v0.1.2 first completion | Reused chunks | Downloaded chunks | Result |
|---|---:|---:|---|
| [Cold v1](evidence/usability-hosted-v0.1.2/summary-1.json) | 0 | 138 | Signed config identity checked against receiver Docker bytes; explicit offline payload `edge-delta-version-1`. |
| [v2 after publisher restart](evidence/usability-hosted-v0.1.2/summary-2.json) | 136 | 2 | Distinct signed config identity and offline payload `edge-delta-version-2`. |
| [Fresh receiver restart](evidence/usability-hosted-v0.1.2/summary-2-restart.json) | 138 | 0 | Fresh loaded completion; origin chunk requests stayed at 2. |

All three completions had zero integrity failures and retries. The 17 gates cover package-only execution on both guests with distinct Docker daemons, registry authentication, publisher-only signing key, inaccessible remote raw HTTP, enrolled/absent/wrong-CA mTLS clients, private runtime-key lifecycle, import without activation, restarts and reuse, removable/replacement proxy, offline execution of both versions with the hub stopped, and uninstall retaining trust/state/data. **Cleanup passed:** `cleanup_errors: []`, `guests_retained: false`; the [cleanup verification](evidence/usability-hosted-v0.1.2/cleanup-verification.json) confirms both owned guests absent and an empty live-guest list.

**Limits:** two Ubuntu 24.04 amd64 OrbStack LXC guests, independent classic `vfs` Docker stores, **shared kernel**—not independent physical hosts, WAN or native-host hardening proof. OrbStack globally clears `LoadCredential`; the disclosed receiver-only fixture restored packaged declarations. This is not untouched stock-unit sandbox or reboot proof. The authenticated registry fixture used **explicitly opted-in HTTP**, not registry HTTPS; receiver-to-hub transport used mTLS. Publisher-to-hub mTLS is not a packaged Make option and was not exercised. Linux arm64 was archive/ELF inspection only; Darwin arm64 was archive/Mach-O inspection only. Docker import did not activate containers: the harness explicitly ran both versions offline.

The [evidence index](evidence/usability-hosted-v0.1.2/README.md) lists selected raw records and the home-path substitution. [Provenance](evidence/usability-hosted-v0.1.2/provenance.json) preserves original and retained hashes separately: original unsanitized result SHA-256 `265bb3be861ad38cf67673390464baccefa90b78b49fcc67f1aea13b1e4d127d` is **not** the hash of the sanitized published result. Bundled documentation remains the release-time snapshot; this documentation-only closeout does not replace release assets or relabel older tests.

#### Historical hosted v0.1.1 acceptance

**Hosted v0.1.1 Linux amd64 acceptance: PASS, 2026-09-20.** Attempt `work/usability-hosted-01` executed the anonymously downloaded release archive, not a local rebuild, and passed all 17 gates. The [retained result](evidence/usability-hosted-v0.1.1/result.json) records archive SHA-256 `6590faf05c1d15da0d54120a2a0ac4e1b703b83ae76092b19a11888f4d15f356` and executable SHA-256 `4807d57b2bb71e3b8b8e1bc23520bb8beb0b418f8eedc8550cb27b67f9fd6bc6`. [Anonymous asset records](evidence/usability-hosted-v0.1.1/anonymous-assets.json) retain five HTTP-200 public downloads with hashes; all four archive/chart entries matched the downloaded [SHA256SUMS](evidence/usability-hosted-v0.1.1/SHA256SUMS). Downloads used `curl -q` without auth headers, netrc, cookies or token environment variables; signed redirect URLs were not retained.

| Hosted first completion | Reused chunks | Downloaded chunks | Result |
|---|---:|---:|---|
| [Cold v1](evidence/usability-hosted-v0.1.1/summary-1.json) | 0 | 137 | Signed config identity checked from receiver Docker bytes; explicit offline payload `edge-delta-version-1`. |
| [v2 after publisher restart](evidence/usability-hosted-v0.1.1/summary-2.json) | 135 | 2 | Distinct signed config identity and offline payload `edge-delta-version-2`. |
| [Fresh receiver restart](evidence/usability-hosted-v0.1.1/summary-2-restart.json) | 137 | 0 | New loaded completion; origin chunk-request count remained 2. |

All three completions had zero integrity failures and retries. The 17 gates cover package-only execution on both guests, distinct Docker daemons, registry authentication, publisher-only signing key, inaccessible remote raw HTTP, enrolled/absent/wrong-CA mTLS clients, private runtime-key lifecycle, import without activation, restarts and reuse, removable/replacement proxy, offline execution of both versions with the hub stopped, and uninstall retaining trust/state/data. Both owned guests were deleted; `cleanup_errors: []`, `guests_retained: false`, and the [independent closeout](evidence/usability-hosted-v0.1.1/closeout-verification.json) confirmed their absence. The [evidence index](evidence/usability-hosted-v0.1.1/README.md) describes the sanitized selected raw records; it does not duplicate this proof narrative.

Scope remains two Ubuntu 24.04 amd64 OrbStack LXC guests with independent classic `vfs` Docker stores and a shared kernel. The fixture restored packaged `LoadCredential` declarations cleared globally by OrbStack; this is not untouched native-host hardening. Arm64 archive members and ELF architecture were inspected only. Publisher-to-hub mTLS is not a packaged Make option and was not exercised; receiver-to-hub mTLS was. Import did not activate containers; the harness explicitly ran them offline.

**v0.1.1 delivery was partial.** Release workflow [35509755784](https://github.com/syscode-labs/edge-delta-lab/actions/runs/35509755784), source `bf56aaf2019189085c6eb9b141e4885e107f4776`, concluded **failure**: `validate` and `github-release` succeeded, but both `containers (client, edgelab-client)` and `containers (daemon, edgelab)` failed. The source-build Dockerfile omitted `hubclient`, causing `no required module provides package example.com/edge-delta-lab/hubclient`. Successful hosted archive/Make acceptance does not prove those GHCR container images were published: the packaged runtime path uses the bundled binary, not the failed source-build path.

**v0.1.2 supersedes the partial v0.1.1 delivery:** its independent hosted acceptance and successful release jobs are recorded above. The immutable v0.1.1 and v0.1.0 assets, tags and historical results remain unchanged.

**Still NOT RUN:** full arm64 receiver lifecycle, independent-kernel/native-host hardening, reboot, remote DNS/firewall access and production certificate rotation/revocation. Successful publication does not establish those runtime boundaries.

#### Why the installer needed a private runtime key

The [original failure](evidence/usability-mtls-20260920/original-0440-failure.json) captured systemd's read-only credential key at `0440`. The shared Go TLS client correctly rejected it; its owner-only validator was **not relaxed**. The installed launcher stages only the client key in the service-owned `0700` RuntimeDirectory, rejecting unsafe directory mode, owner or symlinks. Exclusive temporary creation is `0600` from the first byte; atomic replacement does not follow a destination symlink. Enrollment and source credential remain unchanged, restart refreshes the runtime copy and systemd removes it on stop. The live key was `0600` with receiver UID `64995`.

Regression tests cover `0440` input, unchanged source mode, rotation, destination symlinks, unsafe directory permissions/ownership/symlinks, failed-copy cleanup and the installed launcher contract. Earlier local attempts are not acceptance receipts: attempt 06 found the permission rejection; attempt 07 failed an unprivileged diagnostic read of the fixture's root-private drop-in. The final capture ran as root. Harness repairs retained DNS/SNI verification and normalized archive members for older Python; they did not disable TLS verification.

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

This independent Gear-style chunker is not desync/casync compatible or a full FastCDC implementation. Upstream [design references](docs/ARCHITECTURE.md#design-references) explain concepts; they do not certify this code. Evaluate maintained alternatives before adopting a custom fleet updater.

## Documentation-only validation

The v0.1.2 closeout passed focused checks of all 15 owned documentation/evidence files: JSON parsing, Markdown local links/anchors, privacy patterns, original/retained hash correspondence, gate/cleanup/summary consistency, successful workflow conclusions and whitespace. Five downloaded asset hashes and all four release checksum entries were rechecked against local downloaded bytes; the public release page returned HTTP 200. These closeout checks did not rerun acceptance or change published assets.

For this consolidation, the canonical diagram passed the installed diagram-design self-check and the repository's export/accessibility/geometry checks. Deliberately malformed connector, label-gap and accessible-ID cases were rejected. The actual browser render was visually inspected both as inline HTML (Chakra Petch / IBM Plex Mono loaded) and as a README-style SVG image with fallback fonts; no text clipping or hidden paths was observed. Browser text bounds also stayed inside the frame and nodes.

Local OpenSpec structural checks passed (42 requirements and three preserved historical editable scenes). The retained Linux-evidence verifier passed; it does not rerun Linux services. The original documentation pass checked Markdown local links/anchors and shell-fence syntax before sibling-workstream integration. `MTLS.md` and `GO_EMBEDDING.md` are now integrated; package contracts check bundled documentation link targets. These checks do **not** exercise live services or release download URLs.

## Run checks yourself

These are **reproduction commands, not fresh results**. Use one isolated checkout and run suites serially: overlapping suites previously caused a sender-test harness failure, while isolated serial runs passed. Go must match `go.mod`; Python 3.10+, make and Helm 3 are needed for full packaging/contracts. Docker/Kind tests need an explicitly disposable local daemon; never substitute a production daemon.

```sh
make -j1 check
make openspec-check
python3 scripts/watch_smoke.py
(cd examples/go-embedding && go test -race ./... && go build ./... && go vet ./...)
make build
python3 scripts/demo.py --work work/transport-fresh --size-mib 16 --rate-kbit 5000
make docker-smoke
make docker-demo WORK=work/docker-fresh SIZE_MIB=16 RATE_KBIT=5000
make install-contract
```

Choose fresh work paths. `make check` covers Go vet/tests/race and Python tests, not physical durability. The synthetic demo is transport-only: its generated archives are not runnable Docker images, so never use `--docker-load` with them. `scripts/watch_smoke.py` exercises manual publication, promotion and a persistent staging client; prefer that automated reproduction to a parallel multi-terminal demo. The root `compose.yaml` and `scripts/prepare_compose.py` are fault-injection simulation, not the installation entrypoint. The Docker demo explicitly runs real images offline. Inspect each generated `results.json`, logs and `RESULTS.md`, not just process exit. Sender-side demo visibility uses `make sender-status WORK=work/docker-fresh`; it does not scrape receiver files.

Additional checks:

```sh
# Requires installed official OpenSpec CLI; local structural check is separate.
openspec validate --all --strict --no-interactive
# Requires Docker, Kind, kubectl, Helm and matching Go/GOROOT.
python3 scripts/helm_kind_lifecycle.py --evidence work/helm-kind-fresh
# Static chart contracts; omit --render-only for the isolated Docker exporter check.
python3 scripts/helm_contract_test.py --render-only
# Inspect the historical Linux evidence with its independent verifier.
python3 scripts/verify_sustainability.py
# Local artifacts only: no upload, push or tag.
make release VERSION=v0.1.2
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
python3 scripts/package_smoke.py --archive dist/edgelab-v0.1.2-linux-arm64.tar.gz
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

### Packaged installation acceptance reproduction

The [local acceptance above](#new-usability-and-mtls-acceptance) executed this boundary on disposable OrbStack guests. For a fresh local run, use Go matching `go.mod`, Python 3.10+, make, Helm and the OrbStack CLI, with capacity for two owned Ubuntu guests. The command creates and deletes those guests; never point it at production infrastructure. Keep work directories private: full transcripts and enrollment material are not publication-safe.

```sh
make release VERSION=v0.1.2
(cd dist && shasum -a 256 -c SHA256SUMS)
python3 scripts/usability_e2e.py run \
  --bundle dist/edgelab-v0.1.2-linux-amd64.tar.gz \
  --arm64-bundle dist/edgelab-v0.1.2-linux-arm64.tar.gz \
  --work work/usability-new-attempt --create-guests --delete-guests
make -j1 check
```

Select compatible Go/Python tools through your own environment rather than copying workstation paths; the retained regression log is test output, not a toolchain inventory. Inspect the new `result.json`, every gate and cleanup errors. For hosted acceptance, separately download and checksum the published assets before exercising them; the local-build recipe is not hosted proof.

Manual acceptance checklist:

1. Package and checksum the candidate v0.1.2 source. Record the commit/toolchain and exact extracted bundle members. Use two disposable Linux hosts with independent Docker stores; record kernel, effective unit and storage driver.
2. Follow only [README](README.md) env/Make installation commands. Confirm publisher-only private signing key/registry credentials and receiver-only public signing trust. Keep credentials out of logs.
3. Publish two immutable, single-platform real images. Require signed archive/image identity checks, first loaded summaries and actual offline payload execution using `docker run --pull never --network none IMAGE_ID`.
4. Preserve first cold/update measurements and compare them from the same cache. Restart both sides; verify keys/sequence/cache continuity and no new chunk requests for unchanged content. Run status/stop/start/uninstall and assert retained data.
5. Separately test [mTLS](MTLS.md): unauthenticated request rejected, provisioned client succeeds, private CA/key material remains private, proxy stop/removal does not mutate publisher or hub state. TLS failure must never become an insecure fallback.
6. Remove only owned resources, verify cleanup and retain sanitized command results with clear PASS/FAIL/NOT RUN labels. This recipe is not a receipt that those steps ran.

### Measure a real site safely

Use [HTTP fault controls](examples/flaky.json) only on isolated tests. `rate_kbit` is aggregate body pacing; `latency_ms`, `fail_first`, `drop_every`, `drop_after_bytes`, `corrupt_first`, `stall_first`/`stall_ms`, `offline` and `offline_for_ms` operate at HTTP level, not packet loss. Atomic replacement works natively; a single-file Docker bind mount can retain the old inode.

Compare actual ordinary pulls and delta transfers from equivalent initial caches. Record CPU, elapsed time, disk high-water mark, flash writes and interface bytes, alongside body counters. Vary chunk size/concurrency and test expired credentials, full disks and long outages. Only authorized disposable namespaces/hosts should receive kernel traffic shaping; remove it afterward. Test actual abrupt power-off separately. Never change unrelated routes, firewalls, cloud accounts or Tailscale ACLs to make a test pass.

See [operations](OPERATIONS.md), [architecture](docs/ARCHITECTURE.md) and the [evidence redaction policy](evidence/REDACTION.md) before collecting or sharing results.
