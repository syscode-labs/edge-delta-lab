# Edge Delta Lab

**Deliver Docker image updates to remote machines over slow or unreliable links by downloading only the chunks they do not already have.**

Small changes inside a large image layer can mean another large download. Edge Delta keeps verified pieces of previous deliveries on each machine, reuses them for later updates, and resumes interrupted downloads without losing completed chunks. Docker remains on the device. A cold client downloads the content first; Docker's existing image store is not automatically an Edge Delta cache.

**How it works**

1. **Publish once:** prepare an image archive for delivery and sign the release.
2. **Download missing pieces:** each device checks the release and reuses verified pieces it already has.
3. **Rebuild and verify:** reconstruct the complete archive and check it against the signed release.
4. **Optionally load into Docker:** import the verified image when explicitly enabled. Loading does not start or replace containers.

This is a runnable experiment, **not a production-ready fleet updater**. Start with the local example below: it demonstrates cold and incremental delivery without touching Docker.

<a id="quickstart-local-hub-and-persistent-client"></a>

## Try a cold download, then a small update

You need a source checkout, Go 1.23 or newer, `make`, and a Unix-like host. Linux is the primary validated runtime. Run all commands from the repository root, with port 8080 available and a fresh `work/quickstart` directory.

This example uses small **synthetic archives**, not runnable Docker images.

**Terminal 1 — build, publish the first version, and start the hub:**

```sh
make build
./bin/edgelab keygen --out work/quickstart/keys
./bin/edgelab fixtures --out work/quickstart/fixtures --size-mib 2
./bin/edgelab publish \
  --input work/quickstart/fixtures/app-a-v1.tar \
  --root work/quickstart/origin --key work/quickstart/keys/publisher.key \
  --release app-v1 --sequence 1
./bin/edgelab promote --root work/quickstart/origin --release app-v1
./bin/edgelab serve --root work/quickstart/origin \
  --listen 127.0.0.1:8080 --events
```

Leave the hub running. Its default transfer limit is 5 Mbit/s.

**Terminal 2 — start a client and leave it running:**

```sh
./bin/edgelab watch \
  --manifest http://127.0.0.1:8080/releases/desired.json \
  --base http://127.0.0.1:8080 \
  --state work/quickstart/client --pub work/quickstart/keys/publisher.pub \
  --allow-http --events-url ws://127.0.0.1:8080/events
```

Wait for the JSON summary containing `"release": "app-v1"` and `"phase": "staged"`. In the **first** summary, `reused_chunks` is zero: this client has no cached content yet. Note `downloaded_chunks` and `chunk_response_body_bytes`.

**Terminal 3 — publish the changed version:**

```sh
./bin/edgelab publish \
  --input work/quickstart/fixtures/app-a-v2.tar \
  --root work/quickstart/origin --key work/quickstart/keys/publisher.key \
  --release app-v2 --sequence 2
./bin/edgelab promote --root work/quickstart/origin --release app-v2
```

Return to Terminal 2. In the **first** `"app-v2"` summary, look for reused chunks and fewer downloaded bytes than the cold download. The second fixture changes 32 KiB inside the application payload; the same running client rebuilds and verifies the complete archive without downloading all of it again.

Later summaries describe repeated checks, not the original transfer. `chunk_response_body_bytes` measures chunk response bodies, not total network traffic.

Stop the hub and client with Ctrl-C. Keep `work/quickstart/client` to reuse downloaded content after restarting. To discard this example instead, remove `work/quickstart` after both processes stop. Release names must be unique and sequence numbers must increase; never share client state between concurrent clients.

Plain HTTP is allowed here only for isolated local testing. The client receives the pinned public key, never the signing key.

## Use your own Docker images

Follow [registry publication and client loading](docs/DOCKER_RUN.md#registry-publication-and-client-loading) to publish actual images and explicitly enable Docker import. Do not add `--docker-load` to the synthetic quickstart: importing requires a signed Docker-archive release with expected image IDs. By default the client only stages a verified archive.

## Verification and recovery

- The client verifies the signature, release sequence, chunk hashes and sizes, and complete archive before any Docker import.
- WebSocket notifications are **hints only**: they wake reconciliation, but fetched signed metadata authorizes the content. Polling continues if push is unavailable.
- Completed, verified chunks survive interruptions and are revalidated on recovery. There is **no silent full-image fallback**.
- `staged` means a verified archive; `loaded` means expected image IDs were imported, not that containers are running or healthy. The durable import marker records past import, not current Docker inventory.

See [architecture and integrity boundaries](docs/ARCHITECTURE.md) for the trust model. The [runtime guide](docs/DOCKER_RUN.md) covers registry watching, Docker packaging, and Helm prerequisites. The one-shot `conf` command is not the persistent `watch` daemon.

## Observe delivery

See the [Grafana guide and latest public-safe screenshot](docs/GRAFANA.md) for optional setup, panel meanings, and cold-versus-incremental delivery results.

`make build` also builds `edgelab-exporter`. To enable monitoring, stop the quickstart daemons and rerun their commands with `--admin-socket work/quickstart/hub/admin.sock` added to `serve` and `--admin-socket work/quickstart/client/admin.sock` added to `watch`. Keep their existing state directories. Then start each exporter in its own terminal:

```sh
./bin/edgelab-exporter --socket work/quickstart/hub/admin.sock \
  --listen 127.0.0.1:9109 --role hub --instance hub-01
```

```sh
./bin/edgelab-exporter --socket work/quickstart/client/admin.sock \
  --listen 127.0.0.1:9110 --role client --instance client-01
```

Scrape `/metrics` on those ports with Prometheus or a compatible collector. Metrics cover source availability, served body bytes, verified downloads, reused chunks, retries, integrity failures, and sync outcomes. The exporter reads local Unix admin sockets; clients need no inbound delivery connection. A client exporter still needs a local scraper or an explicitly secured scrape path.

- [Alloy example](deploy/helm/edgelab-hub/edgelab-proof.alloy): adapt its targets and instance labels to your deployment and configure your own remote-write destination. **Grafana Cloud is not required.** The example keeps only `edgelab_.*|up`.
- [Grafana dashboard JSON](deploy/helm/edgelab-hub/dashboards/edgelab-delivery-cache.json): select your Prometheus datasource, replace the proof-specific client instance filter, and choose a current time range. It retains a historical proof window by default.
- Check both scraper `up` and `edgelab_source_up`: an HTTP endpoint answering does not prove the admin source is healthy. Confirm actual samples in your backend before treating installation as monitoring success.

Hub HTTP counters come from the admin socket's `origin` snapshot; cache bytes, objects, capacity, hits and evictions come from its separate `hub` snapshot. An unavailable source reports `edgelab_source_up=0` without publishing zero or stale cache samples. The [optional Helm exporter](docs/DOCKER_RUN.md#helm-kubernetes) uses the same binary and explicitly overrides the image entrypoint.

## Measured cold-to-delta proof

Retained Linux daemon evidence includes a standalone Linux hub delivering over Tailscale to a Linux client with a 5 Mbit/s ingress cap. The Grafana backend retained these observations:

| Observation | Cold release | Changed release |
|---|---:|---:|
| Downloaded chunk body bytes | 16,791,337 | 249,778 |
| Newly verified chunks | 202 | 3 |
| Reused chunks | — | 202 |

Across the proof, successful syncs reached **2**, integrity failures remained **0**, and the series budget remained **38**. All 19 dashboard/series queries returned HTTP 200 and Prometheus success; two post-completion scrapes were retained.

These are measured chunk-body bytes, **not total wire bytes**, an actual registry-pull comparison, or a production-image gigabyte benchmark. Metadata, headers, retransmissions, and VPN overhead are separate. This daemon proof stages synthetic Docker archives; [earlier real Docker evidence](evidence/revision-3/README.md) separately verifies import and offline container execution.

Read [sustainability results](SUSTAINABILITY.md) and [Grafana query evidence](evidence/v4-productization/v31-grafana/README.md). Retained files provide the evidence without requiring access to the original Grafana instance. **NOT RUN:** arm64 runtime execution, matched direct-versus-Tailscale overhead, a longer multi-client packet-loss soak, and authenticated dashboard screenshot/render verification. A bounded proof is not production readiness.

## Security and limitations

- The hub has no integrated TLS or client authentication. Do not expose the lab listener publicly; provide an appropriate secure deployment boundary. WebSocket hints and unauthenticated acknowledgements do not authorize content or establish device identity.
- Signatures and persisted sequence checks protect release acceptance; enrollment, trust rotation, expiration, and complete freeze-attack protection are not implemented.
- The independent Gear-style chunker is not desync/casync compatible. The delivery signature authorizes the archive, not every original Open Container Initiative registry digest, referrer, or signature artifact.
- Chunk-cache/archive retention is unbounded; Docker storage needs additional capacity. SIGKILL recovery is not hardware power-loss proof.
- Docker access is privileged. Loading is not activation or database rollback. The limited one-shot `conf` restart action does not preserve general container configuration and is not a health-gated rollout controller.

See [production gaps](docs/PRODUCTION_GAPS.md) before evaluating a deployment.

## Local release packaging

With Go, Python 3.10 or newer, Helm 3, and `make` installed, run:

```sh
make release VERSION=v0.1.0
```

This clears and recreates `dist/` with exactly:

- `edgelab-v0.1.0-linux-amd64.tar.gz`
- `edgelab-v0.1.0-linux-arm64.tar.gz`
- `edgelab-v0.1.0-darwin-arm64.tar.gz`
- `edgelab-hub-0.1.0.tgz`
- `SHA256SUMS`

Each platform archive contains `edgelab` and `edgelab-exporter` at its root,
built with CGO disabled. The Helm package comes from `deploy/helm/edgelab-hub`.
Packaging fails if the chart's `version` is not `0.1.0` or its `appVersion` is
not `v0.1.0`. `SHA256SUMS` covers all four distributable archives; verify with
`(cd dist && shasum -a 256 -c SHA256SUMS)`.

Archive members are sorted and have fixed ownership, permissions, and timestamps.
`SOURCE_DATE_EPOCH` sets the archive timestamp (default `0`). Reproducible bytes
require the same source, dependencies, Go, Helm, Python, and compression toolchain;
these are local packages, not proof of runtime execution on every platform.
No tag, upload, or publication is performed by `make release`.

The [release workflow](.github/workflows/release.yml) runs only on pushed `v*`
tags. It requires an exact stable `vMAJOR.MINOR.PATCH` tag and matching chart
metadata; prerelease tags and build metadata are rejected by the unchanged local
packaging contract before anything is published. It runs `make -j1 check`,
`make openspec-check`, and the same `make release VERSION=${tag}` command, then
uploads every `dist/` artifact to the GitHub Release for that existing tag.
Using only `GITHUB_TOKEN`, it also publishes these GHCR images for both
`linux/amd64` and `linux/arm64` (owner/repository names are lowercased):

- `ghcr.io/<owner>/<repo>/edgelab:<tag>` — Dockerfile `daemon` target.
- `ghcr.io/<owner>/<repo>/edgelab-client:<tag>` — Dockerfile `client` target.

Both images receive `latest` only for stable versions, never prereleases.
`latest` tracks the last successful stable publication, not a semver-sorted
maximum. Publication jobs run independently after validation; a failure can leave
partial publication. A rerun replaces assets and image tags for the same version.
This local checkout still has no remote: the workflow is source-ready but has
**not been remotely executed**, and no hosted release or GHCR image is claimed.

## Developer demonstrations

The older one-shot Docker demo remains useful for fault injection and import verification, but it is not the product's daemon installation path. It needs Python 3.10 or newer, Go, `make`, and a working Linux test Docker daemon:

```sh
docker version
make check
make docker-demo
```

It builds real images, interrupts delivery, verifies reconstruction and import, runs offline payload probes, and retains `work/docker-demo/RESULTS.md`, `results.json`, and logs. Use a test daemon, not a production host. Repeated runs require a fresh directory, for example `make docker-demo WORK=work/docker-second`. The default payload is 16 MiB, not a completed gigabyte benchmark.

For transport-only fault tests without Docker, use `make demo` (synthetic archives). [Editable diagrams](docs/diagrams/) describe the underlying sender/chunk/recovery experiment, not the full registry-watcher and monitoring deployment. [Source references](docs/SOURCES.md) provide further background.
