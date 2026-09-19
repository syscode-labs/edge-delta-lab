# Is this ready for your devices?

Use this repository to evaluate download savings and recovery on controlled test devices. It has demonstrated verified transfers, reuse of previously downloaded content, recovery after a process kill, and real Docker import and execution.

**Do not use it as an unattended production fleet updater today.** It lacks fleet authentication and enrollment, bounded device storage, health-checked application activation and automatic rollback. The evidence does not establish behavior on production-sized images, under power loss or during prolonged multi-client network failures.

The experiment shows that the transfer approach can work. It does not show that maintaining a custom updater is better than choosing a maintained image-distribution or device-update system.

## Works now

A **chunk** is a piece of an archive downloaded and verified independently. A **manifest** is the signed description of a release and its chunks. The device stores uncompressed chunks in a **cache**, a directory of saved content, so later releases can reuse them. **Immutable** means that a published name cannot later refer to different content. An **artifact** is a produced file or package, such as an image archive; a **benchmark** is a performance measurement under stated test conditions.

| You can… | Implemented support |
|---|---|
| Reuse content even when an edit shifts its position | Content-defined chunking chooses boundaries from data before compression; shared cache and separately compressed, immutable download objects |
| Reject tampered or incomplete downloads | Signed, ordered manifests; bounded decompression; size and hash checks for compressed chunks, uncompressed chunks and the complete archive |
| Resume interrupted work | One writer per device state directory; chunks synced to disk, atomically renamed and directory-synced; per-object retries with increasing delays |
| Test unreliable delivery | Aggregate server rate limit and real response truncation, corruption and server-unavailable periods |
| Check unchanged releases and rebuild offline | Conditional metadata requests using ETags, plus reconstruction from retained verified content |
| Import a verified image into Docker when explicitly enabled | Optional Docker importer; import is separate from application activation |
| Inspect delivery progress | JSON and Prometheus output; a sender-side SQLite collector and CLI; durable lab acknowledgement queue for staged/imported releases and duplicate-safe sender storage |

Reproducible fixtures and retained results support these claims. The [experiments guide](EXPERIMENTS.md) separates tests from assumptions. Lab acknowledgements are not authenticated fleet reports, and **loaded means imported, not running or healthy**.

## Not ready yet

| Missing capability | Why it matters |
|---|---|
| Fleet enrollment and device attestation | No complete process to admit devices and verify their identity or condition |
| Customer-scoped authenticated origin and authenticated fleet acknowledgements | Signed content does not by itself authenticate download clients or progress reports |
| TLS/mutual-TLS certificate rotation | No managed renewal of transport or device-authentication certificates |
| The Update Framework-style trust delegation, expiry and freeze protection; hardware monotonic counters | Current signed ordering is not a complete system for delegating trust, expiring releases or resisting replay with hardware-backed state |
| Managed rollout policies | No fleet rollout controls |
| Cache quotas and garbage collection | Cached content and staged archives are retained indefinitely |
| Separate scheduled integrity scans | Each reconciliation (a check that local content matches the desired release) still verifies the entire referenced cache and archive |
| Health-gated activation, automatic rollback and data migration policy | Import does not safely switch the application, prove its health or reverse database changes |
| Importer privilege separation | The optional host Docker importer is privileged; it is not isolated behind a production privilege boundary |
| Full Open Container Initiative (OCI) referrer/signature propagation | OCI defines container image formats; referrers attach related files, such as signatures, to an image. These are not fully carried through conversion and import. |
| Distribution through wide-area peers and a Harbor publisher integration | These distribution/publishing integrations are not implemented |

The retained results also leave these tests **NOT RUN**: production 1–2 GiB image benchmarks against measured ordinary registry pulls, power-loss/hardware validation, arm64 runtime execution, matched direct-versus-Tailscale overhead and a long multi-client packet-loss soak.

Documentation/tool checks have narrower gaps: the official OpenSpec CLI validator was unavailable (local structural checks only), Excalidraw browser import was not run, and **authenticated automated dashboard capture/render verification** remains **NOT RUN**. A [historical dashboard screenshot is included](GRAFANA.md#latest-supplied-screenshot-and-measured-context), and Grafana panel queries were executed successfully. Neither the screenshot nor these checks replace runtime tests.

## Decisions before production

### Measure savings on the images you actually ship

An image already in Docker's image store does **not** automatically populate the device's chunk cache. The publishing source, transport cache and Docker store are separate. Plan an initial download, or seed the cache by publishing/exporting known content.

A small code change can rewrite most of a compiled or compressed artifact. Measure actual content reuse, compression costs and metadata against ordinary pulls from the same starting cache. The retained benchmarks use small fixtures; the representative 1–2 GiB benchmark remains unmeasured, not a production go-ahead. See the [evidence limits](../evidence/README.md) and [per-check status](../evidence/revision-3/validation-status.json).

### Budget disk space and repeat checks

Keep active, rollback and in-progress content safe before adding automatic cleanup. Budget for both retained cache/staging files and Docker's extra unpacked image storage. Test storage exhaustion during both transfer and Docker unpacking.

Repeated checks currently reread and verify the whole referenced cache and archive. Large images may need an authenticated shortcut for unchanged releases and a separate schedule for integrity scans. Do not reduce verification silently to make checks faster.

### Decide which image identity deployments require

The source-to-delivery identity is recorded, but the archive conversion does not preserve every original compressed manifest digest. A digest is a content hash identifying a particular representation. Deployment by the original registry digest, or reliance on registry signatures and attached artifacts, may require a different importer or image representation. Full Open Container Initiative referrer/signature propagation is not provided.

### Keep transfer costs predictable

A failed HTTP chunk request retries that chunk; it does not resume from a byte offset within it. The default chunk size bounds the retry unit. Add partial-chunk requests only if measurements justify them.

The JSON manifest is readable but verbose. Multi-gigabyte images may need more compact indexes, separate layer indexes and reusable cached index components. These are options to evaluate, not capabilities already demonstrated.

### Choose a maintained chunking engine deliberately

The original design suggested desync. This lab uses its own small format so its behavior could be tested without upstream binaries or library downloads. It is **not evidence of desync's format, durability or performance**.

Compare desync, casync or another maintained content-defined chunking implementation using your actual images. Switching is not a drop-in replacement: desync has its own index and storage conventions. A change needs a new format version, parallel published artifacts and a cache migration or seeding plan. Preserve signatures, durable writes, retries, state recovery and verification throughout.

### Set application and build safety requirements

Test that staging leaves the current application running. Decide how to start the new version, check health, recover from a bad update and handle application data. Image rollback is not database rollback. A process-kill test is not proof of storage safety during power loss.

The transfer core uses Go's standard library, while the complete application also uses external libraries. Deployment depends on those libraries, a compiler/runtime and an operating system. Pin an actively supported compiler, reviewed immutable CI action revisions and container images by digest. Produce a software bill of materials and build provenance. The included CI workflow is a starting point, not a signed production build system; its presence does not prove hosted execution.

## Platform coverage

The product is designed to be OS-agnostic, but acceptance evidence is Linux-first. A built binary is not proof it ran successfully. Long-running hub/client behavior must be observed during idle periods and bad-link tests, not inferred from a foreground test suite.

| Platform or test | What has actually run |
|---|---|
| Local OrbStack Linux VM, linux/amd64 | Real Docker demonstration on OrbStack 29.4.0: three releases at 5,000 kbit/s with disconnects, HTTP 503, corruption and SIGKILL; 5 saved chunks preserved, 0 re-requested. Images ran with `docker run --pull never`, no network and the origin stopped. Separate Docker smoke builds had distinct configuration and layer digests. |
| Local Linux services | Idle and shaped-link measurements with continuously running services; bounded one-client outage recovery. These are local Linux-VM/container results, not standalone-host production proof. |
| Standalone Linux amd64 hub to Linux client over Tailscale | Bounded cold and changed-release delivery with a 5 Mbit/s Linux cap, two successful syncs and zero integrity failures. The hub was on a standalone host; the client was in OrbStack Linux. |
| `linux/arm64` | Static artifact built and inspected; runtime **NOT RUN** |
| macOS arm64 | Coordinated the tests; not production runtime proof |

The earlier “real Docker NOT RUN” limitation is superseded by the [revision-3 Docker results](../evidence/revision-3/README.md). The standalone Linux result is a separate, later test. Neither closes the remaining platform or production gaps.

See [Linux and Grafana measurements](../SUSTAINABILITY.md) for exact scope, and the [test-suite execution note](EXPERIMENTS.md#test-suite-execution-anomaly) for the shared-checkout test failure. That failure was a test-runner issue, not a product service failure.
