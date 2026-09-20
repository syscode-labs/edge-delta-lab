# Edge Delta Lab

**Deliver Docker image updates to remote Linux machines without downloading unchanged chunks again.**

A persistent publisher watches a standard Registry v2 repository; receivers fetch missing chunks, verify the signed archive and optionally load it into Docker. Verified chunks survive restarts. The first delivery fills the cache; existing Docker images do not seed it.

Loading is **not** starting or replacing containers. This is an experimental delivery system, **not a production-ready fleet updater**. No particular registry vendor, VPN, Kubernetes or monitoring service is required.

## Get the Linux bundle

Download the [public v0.1.0 release](https://github.com/syscode-labs/edge-delta-lab/releases/tag/v0.1.0) using the commands below, or obtain a [locally packaged bundle](#local-release-packaging) from a maintainer. Neither path needs Go or a source checkout on the installation hosts.

On each Linux host, download/extract the bundle for its CPU. This example is amd64; use `linux-arm64` for arm64 (packaged, runtime not yet validated):

```sh
curl -fLO https://github.com/syscode-labs/edge-delta-lab/releases/download/v0.1.0/edgelab-v0.1.0-linux-amd64.tar.gz
curl -fLO https://github.com/syscode-labs/edge-delta-lab/releases/download/v0.1.0/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
mkdir edgelab-v0.1.0
tar xzf edgelab-v0.1.0-linux-amd64.tar.gz -C edgelab-v0.1.0
cd edgelab-v0.1.0
```

Requirements: Python 3, Docker Engine; the publisher host also needs Docker Compose, and the receiver needs systemd and sudo. Install on the local daemon host, not through a remote Docker context. The receiver needs a `docker` group with socket access for import. The runtime image assembly downloads Alpine and CA certificates; it does not compile Edge Delta.

## Install a publisher and hub

Choose an existing repository and **immutable, single-platform tags matching the receiver architecture**. Multi-platform indexes are unsupported. Replace these example values:

```sh
./install hub setup --directory "$HOME/edge-delta-install" \
  --registry https://registry.example.net --repository team/app --allow '^v'
"$HOME/edge-delta-install/manage" hub status --directory "$HOME/edge-delta-install"
```

For a password-protected registry, add `--username YOUR_USER --password-file /path/to/private-password-file` to setup. The [installation guide](deploy/compose/README.md) covers Basic authentication, storage and permissions. Setup creates keys/state once and starts both services in the background. Only the publisher receives the signing key and registry credentials; neither service receives a Docker socket.

The hub binds **HTTP on 127.0.0.1:8080 only**. Before connecting remote receivers, provide a persistent HTTPS reverse proxy with a certificate they trust, forwarding to that loopback listener. This is an infrastructure prerequisite, **not installed by Edge Delta**. Keep access within your trusted network; HTTPS alone does not add client authentication. See [TLS and network boundaries](deploy/compose/README.md#tls-and-network-boundaries).

## Connect a receiving machine

Transfer only `edge-delta-install/keys/publisher.pub` to the receiver over a trusted channel (for example, verified SSH). Do not copy the installation directory, private key or registry password. From the extracted Linux bundle on the receiver:

```sh
sudo ./install receiver setup --hub https://hub.example.net \
  --public-key /path/to/publisher.pub --device-id receiver-01 --docker-load
sudo edgelab-manage receiver status
```

This installs and enables a persistent systemd service—no foreground process or terminal to keep open. Docker import grants root-equivalent daemon access; omit `--docker-load` for verification/staging only. HTTPS is required unless you explicitly opt into HTTP with `--allow-http` on an isolated network.

Push an eligible tag, wait for its first `loaded` completion in `sudo journalctl -u edgelab-receiver`, then push a **new version tag**. The same services publish and load the update. Inspect `/var/lib/edgelab-receiver/summary.json` with sudo: `phase`, `downloaded_chunks`, `reused_chunks` and `chunk_response_body_bytes` describe the latest reconciliation, not necessarily the original transfer. Later no-op checks overwrite it. Chunk-body bytes are not total wire traffic.

## Manage the installation

```sh
# Publisher host (substitute status, stop, start or uninstall for restart):
"$HOME/edge-delta-install/manage" hub restart --directory "$HOME/edge-delta-install"
# Receiver (same lifecycle verbs):
sudo edgelab-manage receiver restart
```

Uninstall removes services/binaries or containers, **not trust/state/data**. Back up before explicitly purging. Restart never means rerunning setup or resetting sequence counters. See the [complete lifecycle](deploy/compose/README.md#lifecycle), including retained-state and reinstall boundaries.

## Verification and recovery

- Signature, sequence, chunk hashes/sizes and the complete archive are checked before Docker import; imported image identity is verified before reporting `loaded`. That status does not prove application health or continued presence in Docker.
- Registry discovery and export support Basic authentication. Failed publication is retried on later polls; a digest is committed only after publication/promotion succeeds. Transient channel-promotion failure resumes without overwriting an immutable release. Use new tags for changed content.
- Completed chunks survive interruptions and are revalidated on recovery. There is **no silent full-image fallback**.
- WebSocket notifications are hints, not authority. The installed receiver uses periodic reconciliation; lower-level clients can also enable push hints.

[Packaged Linux installation evidence](evidence/intended-install/packaged-linux/README.md) records two separate Linux systemd environments with independent Docker daemons: authenticated Distribution registry → packaged publisher/hub → HTTPS → packaged receiver, two offline runnable versions and lifecycle checks. Both Linux environments share an OrbStack VM kernel on macOS; this is not a physical remote-site or WAN benchmark. [Earlier same-daemon evidence](evidence/intended-install/README.md) remains separately labeled.

## Optional integrations and deeper guides

- **Helm:** the [Kubernetes hub deployment](docs/DOCKER_RUN.md#helm-kubernetes) replaces the Compose hub; it does not install a registry publisher. Kubernetes is optional.
- **Exporter and Grafana:** [monitor delivery](docs/GRAFANA.md) with the bundled `edgelab-exporter`, Prometheus-compatible storage and the supplied dashboard. Check both scraper `up` and `edgelab_source_up`. Grafana Cloud is not required. The [runtime guide](docs/DOCKER_RUN.md) covers sockets and optional Helm exporter configuration.
- **Architecture:** [integrity/trust model](docs/ARCHITECTURE.md), [production gaps](docs/PRODUCTION_GAPS.md), and [measured sustainability results](SUSTAINABILITY.md). Retained cold-to-delta measurements are chunk-body bytes, not a direct registry-pull comparison.
- **Source/native alternatives:** [runtime guide](docs/DOCKER_RUN.md) and [source setup](deploy/compose/README.md#source-build-alternative).

## Security and limitations

The hub has no integrated TLS, receiver authentication, enrollment or key rotation. Signatures and persistent sequence checks do not implement expiry or complete freeze-attack protection. Do not expose the raw listener publicly. The delivery signature authorizes the archive, not every upstream registry referrer/signature artifact. The independent Gear-style chunker is not desync/casync compatible.

Chunk/cache/archive retention is unbounded; provision space for complete uncompressed archives and Docker storage. SIGKILL recovery is not hardware power-loss proof. Import is not activation, database rollback or health-gated rollout. Multi-platform indexes, arm64 runtime execution and production readiness remain outside the demonstrated boundary.

## Local release packaging

Maintainers need Go matching `go.mod`, Python 3.10+, Helm 3 and `make`:

```sh
make -j1 check
make openspec-check
make release VERSION=v0.1.0
(cd dist && shasum -a 256 -c SHA256SUMS)
```

This recreates `dist/` with `edgelab-v0.1.0-linux-amd64.tar.gz`, `edgelab-v0.1.0-linux-arm64.tar.gz`, `edgelab-v0.1.0-darwin-arm64.tar.gz`, `edgelab-hub-0.1.0.tgz` and `SHA256SUMS`. All platform archives contain `edgelab` and `edgelab-exporter`; Linux bundles also contain `install`, its Python manager and Compose runtime assets. Darwin binaries do not include the Linux installer. No tag, upload or publication occurs locally.

The [release workflow](.github/workflows/release.yml) validates a pushed stable `vMAJOR.MINOR.PATCH` tag and matching chart metadata, runs the same checks/package command, then uploads all `dist/` assets. It also publishes multi-architecture `ghcr.io/<owner>/<repo>/edgelab:<tag>` and `edgelab-client:<tag>` images. Publication jobs can fail independently; local checks do not prove GitHub publication. `latest` tracks the last successful stable publication, not semver order. Archives have normalized metadata; identical bytes require identical source/dependencies/toolchain and `SOURCE_DATE_EPOCH` (default `0`).

## Developer demonstrations

<a id="quickstart-local-hub-and-persistent-client"></a>

The [synthetic transport demo](docs/SYNTHETIC_DEMO.md) and root Compose simulation are developer tools, not onboarding. For real-image fault injection using a disposable test Docker daemon:

```sh
make build
make docker-demo
```

This source-based harness needs Go, Python, make and Docker; it retains `work/docker-demo/RESULTS.md`, `results.json` and logs. Repeated runs need a fresh work directory. Its default payload is 16 MiB, not a gigabyte benchmark. Use `make demo` for synthetic transport-only testing. [Editable diagrams](docs/diagrams/) and [sources](docs/SOURCES.md) provide further background.
