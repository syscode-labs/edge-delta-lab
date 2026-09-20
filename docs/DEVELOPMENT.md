# Native and source development

For the packaged installation, configuration and lifecycle, use [OPERATIONS.md](../OPERATIONS.md). This guide is for source builds, foreground development processes and optional Helm deployment. Do not run an alternative hub beside an installed one on the same port. Test commands, reproduction and coverage live in [TESTING.md](../TESTING.md).

## Build and prepare

Run from a source checkout on a Unix-like host with Go matching `go.mod` and `make`. Docker is needed only for source-container builds or optional image import. Select a Registry v2 image tag that resolves directly to a **single-platform manifest matching the receiver architecture**; multi-platform indexes are unsupported.

```sh
make build
```

Initialize a fresh workspace **once**; retain these directories on subsequent runs:

```sh
./bin/edgelab keygen --out work/registry/keys
mkdir -p work/registry/origin work/registry/watcher
```

<a id="registry-publication-and-client-loading"></a>

## Publish, serve and receive

Save `work/registry/watcher.yaml`, replacing the registry and repository:

```yaml
registry_url: https://registry.example.com
poll: 30s
repos:
  - name: team/app
    allow: "^v"
    ignore: "-rc"
state_file: work/registry/watcher/state.json
publish:
  root: work/registry/origin
  key: work/registry/keys/publisher.key
  channel: desired
  release_prefix: app-
  sequence_file: work/registry/watcher/sequence
```

Tag filters are regular expressions, not version selectors; `ignore` wins. Prefer new immutable version tags. For Basic authentication, add `username` and `password_env`; the latter names a privately supplied environment variable, not a password literal. Keep one publisher/counter per channel and never distribute `publisher.key`.

Start each command in a separate terminal and leave it running:

```sh
# Terminal 1: publisher
./bin/edgelab watch-registry --config work/registry/watcher.yaml
```

```sh
# Terminal 2: hub (port 8080 must be free)
./bin/edgelab serve --root work/registry/origin \
  --listen 127.0.0.1:8080 --events --rate-kbit 5000 \
  --admin-socket work/registry/hub/admin.sock
```

```sh
# Terminal 3: staging client
./bin/edgelab watch \
  --manifest http://127.0.0.1:8080/releases/desired.json \
  --base http://127.0.0.1:8080 \
  --pub work/registry/keys/publisher.pub --state work/registry/client \
  --allow-http --events-url ws://127.0.0.1:8080/events --poll 30s \
  --admin-socket work/registry/client/admin.sock
```

These HTTP/WebSocket URLs are **loopback-only development settings**. Remote receivers need a trusted HTTPS boundary and independently transferred public key; see [network security](../OPERATIONS.md#tls-and-network-boundaries). The hub's `--events` enables caching and notifications; polling continues without notifications. `--rate-kbit 5000` caps aggregate response-body traffic at 5 Mbit/s (`0` is unlimited).

Wait for `phase: staged` before publishing the next version. For optional import, check `docker version`, stop the staging client with Ctrl-C, and rerun its command with `--docker-load` added on the compatible Linux Docker host. Docker access is root-equivalent. `loaded` means verified import, **not running or healthy containers**.

Stop foreground processes with Ctrl-C. Restart with the same commands, not key generation. Preserve publisher keys/counters and client state; never share a state directory between concurrent clients or reset it to bypass verification. Cache/archive retention is unbounded.

### One check from YAML

`edgelab conf --config FILE` runs once, not as a daemon. Start from [client.yaml](../examples/client.yaml). Its default `action: dry-run` still downloads and stages content; `action: load` imports into Docker. Accepted `poll` and `events_url` fields do not make `conf` persistent: use `watch` for that. The limited `restart` action recreates matching containers without preserving general ports, environment or mounts; it is not a production rollout controller.

## Containerized hub alternative

Replace **only** the native hub; keep the publisher and receiver. Build `make container-multi`, stop the native hub to free port 8080, and ensure the image's non-root UID 100 can read the origin:

```sh
docker run -d --name edgelab-hub -p 127.0.0.1:8080:8080 \
  -v "$PWD/work/registry/origin:/origin:ro" -v edgelab-hub-state:/state \
  edge-delta-lab:local serve --root /origin --listen 0.0.0.0:8080 \
  --events --rate-kbit 5000 --admin-socket /state/admin.sock
docker exec edgelab-hub edgelab-tui --socket /state/admin.sock --json stats
```

Keep `--events`: the image's default command alone does not enable caching or notifications. No Docker socket is mounted; the client image has no Docker CLI, so use the native receiver for import. `docker rm -f edgelab-hub` removes the container but retains its named state volume. Only after removal, and only to discard that hub's saved admin history, use `docker volume rm edgelab-hub-state`. Keep the origin and publisher/client state.

## Source-built Compose alternative

This replaces the native publisher and hub above; the receiver remains separate. With Python 3 and local Docker Compose, create a **new** installation directory:

```sh
go build -o bin/edgelab ./cmd/edgelab
python3 deploy/compose/setup.py --directory "$HOME/edge-delta-source" \
  --registry https://registry.example.net --repository team/app --allow '^v'
docker compose -f "$HOME/edge-delta-source/compose.yaml" up -d --build
```

The daemon image builds from this checkout, which must remain at its original location for rebuilds. With `--username`, privately export `REGISTRY_PASSWORD` for each Compose invocation; this source setup does not persist a password file. Use Compose `ps`, `restart`, `stop`, `up -d`, and `down` against the generated file. `down` removes containers/network, not the installation's bind-mounted data. The new installation has its own public key: enroll the separate receiver with that key and the appropriate hub URL.

<a id="helm-kubernetes"></a>

## Optional Helm hub

The [chart](../deploy/helm/edgelab-hub/) replaces **only the hub**. Keep a separate publisher and clients; do not give the hub a signing key or Docker socket. Build the daemon image with `make container-multi`, then make that image available to your cluster under `YOUR_REPOSITORY:YOUR_TAG`.

Before installing, arrange Helm/cluster access and compatible storage:

- The chart requests a 5 GiB origin PVC and a 1 GiB state PVC, both `ReadWriteOnce` (one node, not necessarily one pod).
- Populate the origin from a separate publisher or copying process; the hub mounts it **read-only**.
- Ensure UID 100 can read origin data and write state. Custom images/storage providers may require security-context overrides or pre-provisioned ownership.

```sh
helm upgrade --install edgelab-hub deploy/helm/edgelab-hub \
  --namespace edgelab --create-namespace \
  --set image.repository=YOUR_REPOSITORY --set image.tag=YOUR_TAG \
  --set hub.events=true --set hub.rateKbit=5000 \
  --set exporter.enabled=false
```

The single hub has an internal ClusterIP service on port 8080. Arrange a secure path before connecting external receivers. Caching remains enabled with `hub.events=false`; only WebSocket hints are disabled. The default `hub.rateKbit=0` is unlimited. Metrics are disabled by default; [optional monitoring](GRAFANA.md) covers the independent exporter image settings and collector setup.

**Storage safety:** Helm uninstall deletes both chart-managed PVCs; a `Delete` reclaim policy can delete the underlying data. Back up origin and state first. Upgrades/restarts/rollbacks retain claims, not uninstall. An older `ReadOnlyMany` origin claim cannot change its immutable access mode in place: plan a backed-up migration/recreation, not a forced upgrade or deletion of live data.

```sh
# Only after backing up the deployment's data:
helm uninstall edgelab-hub --namespace edgelab
```

See [TESTING.md](../TESTING.md) for chart checks and the exact retained lifecycle proof; these commands alone do not establish runtime success.
