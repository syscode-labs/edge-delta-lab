# How an image update reaches a device

This page explains what happens between publishing an image and receiving a verified copy, and which checks protect your device along the way.

You publish an image; a separate publisher prepares an archive and signs its release details.
The hub makes those details and the archive's small pieces available to clients.
An optional notification tells a client to check for an update sooner.
The client checks the signature, reuses verified pieces it already has, and downloads what is missing.
It rebuilds the complete archive and checks its exact bytes before using it.
If you explicitly enable Docker import, the client loads the archive into the local Docker daemon.
Loading makes an image available; it does not start a container or prove the application is healthy.

![Registry to publisher to hub, optional mTLS proxy, receiver and Docker; only the publisher signs and the receiver independently verifies.](diagrams/architecture.svg)

[Editable HTML source](diagrams/architecture.html). The optional mTLS proxy can be removed without changing publication, signing or verification; replace it with your existing trusted HTTPS boundary. It does not hold the release-signing private key.

The client downloads from the hub. Docker does not pull from the hub, and the hub is not a Docker registry. The [README](../README.md) is the starting point for trying this flow. The [runtime guide](DOCKER_RUN.md) covers publication and deployment commands.

## 1. The publisher prepares and signs a release

`watch-registry` is a separate, continuously running publisher. It checks selected repositories for eligible tag changes, fetches the image, and exports a Docker-loadable archive. The fetched image must match the registry hash observed by the watcher; a tag changing during that step causes an error. Use a new version tag for each release because an existing release name cannot be overwritten. Manual publication is also available.

The publisher divides the archive into small pieces called **chunks**. It chooses split points from the content rather than fixed file offsets, so unchanged data can remain reusable after an insertion shifts later bytes. Each piece is compressed separately with gzip. This lets a client download only the pieces it lacks instead of fetching the entire archive again.

A **hash**, also called a **digest**, is a fingerprint calculated from bytes. Each piece has a SHA-256 hash and exact size for both its uncompressed bytes and its compressed download. The release description, called the **manifest**, lists the pieces in order and records the entire archive's hash and size.

The publisher signs that description with its private Ed25519 key. The signed fields include the release name, increasing sequence number, format version, splitting settings, archive identity, ordered piece descriptions, optional Docker image configuration IDs, and source information. The signature wrapper preserves the exact signed bytes using base64, a way to carry bytes in JSON text.

**The promise is the exact published archive, not merely an equivalent filesystem.** Compression happens centrally; the client verifies and decompresses the published bytes. It does not recreate a registry's original compressed layers or their hashes. Changing the compressor can change download hashes without invalidating reusable uncompressed pieces. Receiving a release does not depend on different Go versions producing identical gzip output.

The current splitting rule is `gear64-lab-v1`. It fixes the Gear table seed and boundary rule; it is an independent implementation, not the full FastCDC algorithm. Gear chooses where to split, not whether bytes are trustworthy. Defaults are a 16 KiB minimum, 64 KiB target, and 256 KiB maximum. The target is a tuning parameter, not a guaranteed average piece size.

A release can contain more than one image, with every expected image ID signed. The sample per-image fixtures share one device-level sequence stream to exercise reuse between images. They are not a database that independently tracks the desired version of every application.

Source: [registry publisher](../internal/registry/publish.go), [archive publication](../internal/lab/publish.go), [release format](../internal/lab/format.go), [splitting rule](../internal/lab/chunker.go).

## 2. The hub serves the published release

The publisher writes all referenced pieces before writing the signed release description. Download files are named by their compressed-byte hashes. A release name cannot later refer to different signed content: this is what **immutable release** means here.

To select what clients should receive, the publisher copies an already signed release description to `releases/desired.json`. That named selection is a **channel**. Updating it is **channel promotion**: changing the selected release, not rebuilding its content. The replacement is atomic, so readers see the old or new file rather than a partially written one. Use one publisher and sequence stream per channel.

The hub reads this published-file directory, also called the **origin**, and serves its files over HTTP. It does not need the signing key. In hub mode, simultaneous requests for the same object share a disk read, and an in-memory **cache** keeps recently read bytes for reuse. This reduces repeated disk work when several devices request the same update. The client still checks every release independently.

When configured, one shared rate limiter covers metadata and piece response bodies across workers. It limits application body traffic, not all network packets. The test server can also inject HTTP 503 errors, delays beyond request deadlines, damaged bytes, cut-off responses, or outages. Fault choices follow deterministic request counters, but concurrent scheduling and timing are not exactly reproducible.

Source: [hub cache](../internal/hub/hub.go), [HTTP server](../internal/lab/server.go), [command wiring](../cmd/edgelab/main.go).

## 3. A notification can wake the client sooner

With events enabled, the hub watches publication changes and sends WebSocket announcements. A client configured with `--events-url` checks an announcement's signed contents against its pinned public key before waking its update loop. It then fetches the release description from its configured HTTP URL and verifies it again.

**A notification is only a hint, not trusted delivery or permission to load an image.** Polling remains the fallback when notifications are absent or missed. The `watch` client checks once on startup, then checks again on its polling interval or an accepted hint. This keeps the update path usable without a persistent notification connection.

The hub and `watch` client are long-lived services. One-shot `sync`, `conf`, publication, and demo commands are supporting tools; successful one-shot runs do not prove behavior through idle periods or outages.

Source: [client update loop](../cmd/edgelab/main.go), [announcement checks](../internal/push/push.go), [publication watcher](../internal/push/trigger.go).

## 4. The client verifies the release and downloads only missing data

The client starts with a public key provisioned separately from the hub. It verifies the signature before planning downloads. Limits apply to the signed wrapper, complete archive, each compressed response, and each decompressed piece. A filename alone never proves that local content is correct.

Before fetching pieces, the client saves the highest accepted sequence number and the signed description's hash. It rejects an older sequence or a different description at the same sequence. To authorize a return to old content, publish a newly signed release with a higher sequence number. An interrupted download therefore cannot make an older release acceptable again.

This is a limited rollback rule, not a complete secure-update framework such as The Update Framework (TUF). Expiration, delegated trust, and protection against a server indefinitely withholding a newer release are not implemented. Restoring an old filesystem snapshot can also restore the old counter; there is no hardware-protected monotonic counter.

The client's **cache** is a directory of previously verified, uncompressed pieces, named by their raw-byte hashes. The client hashes the actual cached bytes and checks their sizes against the signed description. It downloads missing or invalid pieces, checks compressed size and hash, decompresses within the expected size limit, and checks raw size and hash before saving them.

### What survives a broken link or restart

Completed pieces survive an interrupted transfer. An incomplete piece is discarded and retried from its beginning; there is no byte-range resume within a piece. With the default maximum of 256 KiB of raw data, this keeps individual retries small. It does not bound TCP retransmissions on an arbitrarily bad link.

Retries wait progressively longer, with a capped delay and random variation to avoid synchronized clients. Numeric `Retry-After` values are honored. Each update attempt has finite per-object retry budgets by default; `watch` supplies later attempts. Cancellation stops workers, and verified cache progress remains reusable.

Missing files (HTTP 404), authentication failures, redirects, and responses exceeding signed size limits fail the attempt. **There is no silent fallback to a whole-image download.** Disk failures stop the attempt rather than causing a repeated download loop.

### What makes a saved piece count as complete

The client writes to a temporary file in the destination directory, syncs it to storage, closes it, renames it atomically, and syncs the parent directory. Newly created directory entries are synced too. Only after these steps succeed is a piece reported as committed. This relies on storage honoring sync requests.

The progress journal is a small atomically replaced JSON file, not SQLite. An operating-system file lock (`flock`) prevents concurrent writers to the same state directory. Leftover temporary files are discarded on restart. Recovery rechecks cache bytes; the journal is never proof that a piece exists.

Source: [client verification and retries](../internal/lab/agent.go), [size and hash checks](../internal/lab/format.go), [durable writes and locking](../internal/lab/storage.go).

## 5. The client rebuilds and checks the complete archive

Downloading and assembly are separate steps. Once all pieces are available, the client writes them in the signed order and verifies the entire archive's exact size and SHA-256 hash. An existing archive is reusable only after the same whole-file check. Successful staging means a verified archive is available on disk; Docker has not been touched.

If assembly is interrupted, the client can rebuild locally from completed pieces without another transfer over the remote link. It does not save partial assembly offsets. The completed archive remains available to retry Docker import after a process or daemon failure.

Before downloading, the client checks available space for missing raw pieces, an archive if it must be rebuilt, and a reserve. That estimate does not cover Docker's additional unpacked storage or the publisher's compression scratch space. It can be conservative when old staging files remain.

Plan disk capacity explicitly: the lab keeps all pieces and archives, has no cache quota, and does not automatically delete data. Production use needs a retention policy that protects active and rollback content before removing unneeded files.

Source: [space checks and assembly](../internal/lab/agent.go).

## 6. Docker import is optional and does not start containers

The transfer itself does not depend on Docker. To import, explicitly enable `--docker-load` on a native client beside a compatible Linux Docker daemon, with the Docker CLI installed and access granted. The release must be signed as `docker-archive` and list expected image configuration IDs: hashes of each image's configuration bytes. Use images matching the receiver architecture.

Only after signature, piece, and whole-archive checks does the client run `docker image load`. It then saves the reported loaded images back out of Docker and checks that the stored configuration bytes match every signed expected ID. A successful CLI exit alone is not enough. The client never patches Docker's internal storage directories.

The delivery signature authorizes this archive. Import does not promise to preserve every source repository-digest association or registry signature file.

The separate archive-normalization helper supports raw and gzip layer members. It checks each uncompressed layer's expected hash (its **DiffID**) without extracting and repacking the layer's filesystem. It rejects archives containing only the Open Container Initiative (OCI) layout, a standard container image format, or requiring zstd layer decoding; use Skopeo to produce a suitable Docker archive for those inputs.

**Loaded is not running or healthy.** Application activation remains outside the core transfer flow. The Docker smoke test separately starts an image by verified ID, with pulls and networking disabled. The separate, one-shot `conf` command has a limited explicit `restart` action, but it does not preserve general ports, environment, or mounts and is not a production rollout controller. Keeping an old image also does not undo persistent-data schema changes.

Source: [import and stored-image checks](../internal/lab/agent.go), [archive-normalization helper](../scripts/normalize_archive.py), [separate config actions](../internal/clientconf/runner.go). See the [runtime guide](DOCKER_RUN.md) for deployment requirements.

## When nothing has changed

The client can ask whether the release description has changed using HTTP `ETag` / `If-None-Match`. A `304 Not Modified` response omits the body. The client uses that request only when it already has an authenticated saved description matching its accepted state. A 304 reuses those signed bytes; it is not unsigned authorization. Changed releases still send the full signed JSON description, whose list of pieces grows with archive size.

With Docker loading enabled, an unchanged signed description skips repeat import only when durable state records both successful import and stored-image verification for that exact description. Changed releases import once; failed or interrupted imports without durable completion are retried conservatively.

The saved marker records a past import, not current Docker inventory or application health. Deleting an image outside this tool or switching Docker daemons is not automatically repaired by that marker.

Every update check still verifies the signature and sequence and re-hashes referenced cache entries and the archive. This favors straightforward integrity checks over low disk and CPU use. Avoid checking gigabytes every few seconds: use a longer interval. Faster conditional checks with separate periodic integrity scans, and smaller metadata indexes, remain future optimizations.

Zero response-body bytes does not mean zero network traffic. HTTP headers, WebSocket keepalives, TCP/IP, and VPN packets still matter on a constrained link.

Source: [conditional requests and import marker](../internal/lab/agent.go).

## Operating and trusting the services

Use the server on an isolated lab network. It has no client authentication or built-in TLS. Release signatures protect authenticity and integrity, not confidentiality or availability. Only allowed chunk/release path shapes are served, but the origin and client cache must still be trusted, operator-owned directories without attacker-controlled symlinks.

Keep the private signing key with the publisher, never on a client or in a public origin volume. Provision the client's pinned public key through a separate trusted route.

An **administrative socket** is a local Unix socket used by the console and metrics exporter to read service status; it is not a network management port. Hub bytes-served counters describe sending, not proof that a client verified or loaded an image. Lab receipts are unauthenticated and are not security evidence. Server health likewise does not prove successful delivery.

The Docker socket is a different interface and grants highly privileged host access. Import must remain explicit. The Compose simulation does not mount that socket, and the shipped client image has no Docker CLI. A production design should separate Docker privileges from network-facing code where practical.

Source: [HTTP server](../internal/lab/server.go), [local status server](../internal/admin/server.go), [runtime guide](DOCKER_RUN.md).

## Tests and scope

[TESTING.md](../TESTING.md) is the single current home for retained proof, measurement definitions and remaining limits. [Operations](../deploy/compose/README.md) covers installation; [MTLS.md](../MTLS.md) covers the optional transport wrapper. Neither a transport certificate nor a WebSocket hint replaces release-signature verification.
