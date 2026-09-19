# Deliver images with a persistent hub and client

Use this guide to publish images from a registry, keep a hub serving them, and run a client that checks for updates. The client downloads and verifies an archive first; loading it into Docker is a separate, optional step. Later sections show how to run the hub in Docker or Kubernetes.

Edge Delta is a runnable experiment, not a production-ready updater. A **publisher** signs release data with a private key. The **hub** serves that data and the image pieces from an **origin directory**. Each client receives only the publisher's public key and uses it to verify downloads independently.

## 1. Prepare your machine

- Use a source checkout, Go 1.23 or newer, `make`, and a Unix-like host. Run commands below from the repository root.
- Keep port 8080 free. Stop the README quickstart hub if it is still running.
- **Registry image restriction:** tags must resolve directly to a **single-platform image manifest** for the receiving machine's architecture. Multi-platform image indexes / manifest lists are **not supported**, even if they contain that architecture. Choose a dedicated single-platform tag before proceeding.
- Start with a fresh `work/registry` directory. On subsequent runs, keep its keys, publisher state, sequence counter, and client state; do not regenerate them as a routine restart step.
- Docker is needed only for image loading or container deployment. Use a compatible Linux Docker daemon and a Docker CLI with explicit permission to access it. Docker access is privileged.

Linux is the primary validated runtime. Linux arm64 artifacts have been built and inspected, but arm64 runtime execution remains **NOT RUN**. See the [retained results](../SUSTAINABILITY.md) for the scope of runtime testing.

Build the host programs:

```sh
make build
```

The local examples use plain HTTP on loopback. The hub has no integrated TLS or client authentication: do not expose this listener publicly. For another host, provide a secure deployment boundary, change the client URLs accordingly, and supply the trusted public key separately from the download connection.

<a id="registry-publication-and-client-loading"></a>

## 2. Choose what to publish

### Publish your own registry image

This is the main path through this guide. `watch-registry` is a long-running publisher that checks selected image tags for changes. Use a new version tag for each release: release names come from the repository and tag, and an existing release cannot be overwritten with changed content. It is separate from the hub and is not installed by the Helm chart.

Create the signing keys and working directories once:

```sh
./bin/edgelab keygen --out work/registry/keys
mkdir -p work/registry/origin work/registry/watcher
```

Save this as `work/registry/watcher.yaml`. Replace the registry URL and `team/app` with your repository. This example assumes anonymous read access:

**Every selected tag must point directly to a single-platform image manifest, not a multi-platform image index or manifest list.** The watcher does not select a platform from an index. Digest verification remains strict; do not bypass a `digest drift` error to publish an unsupported tag.

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

The tag filters are regular expressions: this example includes tags beginning with `v`, except those containing `-rc`. The ignore filter wins. If credentials are required, the configuration accepts `username` and `password_env`; the latter names an environment variable containing the password, not the password itself.

A **channel** is a named file that points clients to the currently selected signed release. Here it is `desired`, served at `/releases/desired.json`. The publisher creates signed Docker archives, records the expected Docker image IDs, and updates that channel automatically.

Use one publisher and one increasing sequence counter per channel. Do not mix manual releases with a fresh watcher counter: clients persist sequence checks to reject older releases.

In **Terminal 1**, start the publisher and leave it running:

```sh
./bin/edgelab watch-registry --config work/registry/watcher.yaml
```

Until it successfully publishes an eligible image, the channel has no release for a client to download. Keep `publisher.key` on the publishing machine; never copy it to a client.

## 3. Run the persistent hub

In **Terminal 2**, start the hub against the publisher's origin directory:

```sh
./bin/edgelab serve --root work/registry/origin \
  --listen 127.0.0.1:8080 --events --rate-kbit 5000 \
  --admin-socket work/registry/hub/admin.sock
```

Leave it running. `--events` enables caching and WebSocket update notifications. `--rate-kbit 5000` limits aggregate response-body traffic to 5 Mbit/s; `0` means unlimited. The admin socket is a local Unix socket for status tools, not an open network port. Its event log persists beside it.

The hub serves published files; it does not run the registry publisher or own the signing key.

## 4. Run a client that watches the channel

In **Terminal 3**, start the client:

```sh
./bin/edgelab watch \
  --manifest http://127.0.0.1:8080/releases/desired.json \
  --base http://127.0.0.1:8080 \
  --pub work/registry/keys/publisher.pub --state work/registry/client \
  --allow-http --events-url ws://127.0.0.1:8080/events --poll 30s \
  --admin-socket work/registry/client/admin.sock
```

The **manifest** is the signed release description: it identifies the archive and the pieces, called **chunks**, needed to reconstruct it. A **hash** is a fingerprint calculated from bytes to check that they match the signed description. The **cache** holds verified chunks for reuse. The client checks the channel every 30 seconds and can check sooner after a WebSocket notification. **Notifications are hints, not authority:** the client retrieves and verifies signed release data independently. Polling continues if notifications are unavailable.

Wait for a JSON summary with `"phase": "staged"`. This means the client has reconstructed and verified the archive; Docker has not been changed. The client checks signatures, sequence, chunk sizes and hashes, and the complete archive. It never silently falls back to downloading a whole image.

Keep the client running to receive later publications. Keep `work/registry/client` across restarts to reuse verified chunks and resume interrupted downloads. Do not share one state directory between concurrent clients. Cache and archive retention is currently unbounded, so plan for disk growth.

## 5. Optionally load the verified image into Docker

Do this only for real Docker images published with signed `docker-archive` metadata and expected image IDs. The registry publisher above supplies both; the synthetic quickstart does not.

On the receiving machine, check Docker access:

```sh
docker version
```

Stop the staging client with Ctrl-C, then rerun its command with `--docker-load` added. For the local registry example, the complete command is:

```sh
./bin/edgelab watch \
  --manifest http://127.0.0.1:8080/releases/desired.json \
  --base http://127.0.0.1:8080 \
  --pub work/registry/keys/publisher.pub --state work/registry/client \
  --allow-http --events-url ws://127.0.0.1:8080/events --poll 30s \
  --admin-socket work/registry/client/admin.sock --docker-load
```

Run this native client beside the compatible Linux Docker daemon, with images matching the receiver's architecture. For a remote receiver, change the URLs and public-key path as described above.

Verification happens before import. A `loaded` result means the expected image IDs were imported. **Loading does not start or replace containers, and does not prove they are healthy.** Manage running containers separately.

For an unchanged signed manifest, the client skips another import only when its persistent state records a completed load and stored-image identity proof for that exact manifest. Every check still verifies signatures, sequence, chunks, and the archive. Changed releases are imported; failed or interrupted imports without durable completion are retried. The completion marker records past import, not today's Docker inventory: deleting an image or switching Docker daemons is not automatically repaired.

### Alternative: try delivery without a registry

Use the [local quickstart](../README.md#quickstart-local-hub-and-persistent-client) for small synthetic archives. It includes manual publication, a hub, and a persistent client. Those archives are not runnable Docker images, so do not enable Docker loading for them. Use its `work/quickstart` paths instead of this guide's `work/registry` paths.

### Alternative: one check from a YAML file

`edgelab conf --config FILE` runs once; it is not a daemon. Its default `action: dry-run` still downloads and stages content but leaves Docker untouched. `action: load` imports it. Although the parser accepts `poll` and `events_url`, these do not make `conf` persistent; use `watch` for that.

The limited `restart` action recreates matching containers without preserving general ports, environment, or mounts. Do not use it as a production rollout controller.

## 6. Run the hub in a container instead

Keep the publisher and native client from the previous steps. Replace only the hub. You need Docker with Linux containers and a published origin readable by the container's non-root `edgelab` user.

Build images locally from this checkout:

```sh
make container-multi
```

This builds `edge-delta-lab:local` for the hub and `edge-delta-lab:client` for clients. It does not build host binaries; that is what `make build` does. Both images use `/state` for writable data.

These commands use local images, not an assumed public download. The repository's release workflow is source-ready, but remote publication has not been executed because this checkout has no remote; no hosted release or GHCR image is claimed.

Stop the native hub with Ctrl-C to free port 8080, then run:

```sh
docker run -d --name edgelab-hub -p 127.0.0.1:8080:8080 \
  -v "$PWD/work/registry/origin:/origin:ro" \
  -v edgelab-hub-state:/state \
  edge-delta-lab:local serve --root /origin --listen 0.0.0.0:8080 \
  --events --rate-kbit 5000 --admin-socket /state/admin.sock
```

The origin is mounted read-only. The named Docker volume `edgelab-hub-state` keeps the admin event log across container replacement. Keep `--events`: the image's default command alone does not enable caching or notifications.

No Docker socket is mounted into the hub. The client image has no Docker CLI and no default subcommand; host networking does not grant Docker access. Use the native client for Docker loading rather than assuming the client image can import.

<a id="helm-kubernetes"></a>

## 7. Deploy the hub with Helm instead

Helm installs Kubernetes resources from the supplied [chart](../deploy/helm/edgelab-hub/). The chart installs **only the hub**, not the publisher or clients. Its install, upgrade, pod restart, rollback and uninstall lifecycle has been [verified on disposable Kind with local-path storage](../evidence/helm-kind-lifecycle/README.md). This is not proof of production storage portability or signed delivery through Kubernetes.

Before installing, arrange:

- Helm and access to your Kubernetes cluster.
- A built hub image available to the cluster. Replace `YOUR_REPOSITORY` and `YOUR_TAG` below with that image.
- Compatible persistent storage. The chart requests a 5 GiB origin volume and a 1 GiB state volume, both with `ReadWriteOnce` access. These are Kubernetes persistent volume claims (PVCs). The origin claim must be writable for a separate publisher to populate it; the hub's mount remains read-only. `ReadWriteOnce` restricts mounting to one node, not one pod.
- A way to populate the origin volume with signed releases. The hub mounts it read-only; publishing or copying files into it is your responsibility.
- Permissions for the non-root process to read origin data and write state. The chart explicitly selects UID 100, the supplied daemon image's `edgelab` user, so kubelet can enforce `runAsNonRoot`. Custom images or storage providers may require security-context overrides or pre-provisioned ownership; this does not prove permissions on every CSI driver.

Once those requirements are satisfied, install one hub replica with an internal-only ClusterIP service on port 8080:

```sh
helm lint deploy/helm/edgelab-hub
helm upgrade --install edgelab-hub deploy/helm/edgelab-hub \
  --namespace edgelab --create-namespace \
  --set image.repository=YOUR_REPOSITORY --set image.tag=YOUR_TAG \
  --set hub.events=true --set hub.rateKbit=5000 \
  --set exporter.enabled=false
```

Arrange a secure path to that service before pointing external clients at it. The chart always enables caching with `--hub`. Setting `hub.events=false` disables WebSocket hints without disabling caching. The default `hub.rateKbit=0` means unlimited response-body traffic; the command above explicitly limits it.

Metrics are disabled by default. To enable them, set `exporter.enabled=true` and set both `exporter.image.repository` and `exporter.image.tag` to your built hub image; they are independent of `image.*`. The exporter is a second container that reads the admin socket and exposes metrics on service port 9108. It explicitly starts `/usr/local/bin/edgelab-exporter` instead of the image's normal entrypoint. No ServiceMonitor or PodMonitor scraper configuration is supplied. See the [Grafana setup and troubleshooting guide](GRAFANA.md).

### Run the real disposable cluster test

With a local Docker daemon, Kind, Helm and kubectl installed:

```sh
python3 scripts/helm_kind_lifecycle.py --evidence work/helm-kind-run
```

Choose a new evidence directory for each run. The test builds the daemon and host CLI from source, generates a small signed synthetic release with `keygen`, `fixtures`, `publish` and `promote`, and loads only published objects into the chart-managed origin PVC through a separate short-lived writable loader pod. The hub mount stays read-only. A real in-cluster `edgelab watch` client uses only the publisher public key against the Helm Service; the gate requires `phase: staged` and a reconstructed archive SHA-256 and size matching the host fixture. The private signing key never enters Kubernetes. All origin objects are checked over HTTP again after upgrade, restart and rollback, alongside exporter metrics and state persistence. Finally it uninstalls the release and deletes its uniquely owned cluster, image and temporary host state. It uses a private temporary kubeconfig, not your current Kubernetes context. This proves signed synthetic archive delivery, not Docker import/activation, an image-version upgrade, or production CSI/multi-node durability. Go is also required for the host CLI build; its executable and `GOROOT` must refer to the same toolchain.

**Storage and upgrade warning:** Helm uninstall deletes both chart-managed PVCs; with a `Delete` reclaim policy this also deletes their data. Back up origin and state before uninstalling a real deployment. PVCs survive the tested upgrade, restart and rollback, not uninstall. Existing installations of the older `ReadOnlyMany` origin claim cannot change that immutable access mode in place: plan a backed-up volume migration/recreation, rather than forcing a Helm upgrade or deleting live data. The default claim now uses `ReadWriteOnce`.

### Check the chart without installing it

Render either configuration locally:

```sh
helm template edgelab-hub deploy/helm/edgelab-hub --set exporter.enabled=false
helm template edgelab-hub deploy/helm/edgelab-hub --set exporter.enabled=true
```

For the automated chart checks, install Python and PyYAML alongside Helm, then run:

```sh
python3 scripts/helm_contract_test.py --render-only
```

Remove `--render-only` to also build the daemon image and check exporter startup with Docker. The script reports a runtime skip if no daemon is available. Its isolated exporter check expects `source_up=0` because no admin socket is mounted. Neither rendering nor this isolated check proves a live cluster installation, working storage permissions, or successful delivery.

## 8. Check progress, stop, or clean up

Check that the local hub is answering:

```sh
curl -fsS http://127.0.0.1:8080/healthz
```

For the containerized hub, read its admin status:

```sh
docker exec edgelab-hub edgelab-tui --socket /state/admin.sock --json stats
```

| What you see | What to check next |
| --- | --- |
| `/healthz` succeeds, but nothing downloads | Health only means the server is available. Check the publisher output, tag filters, and whether `releases/desired.json` exists in the origin. |
| Client reports `staged` | Delivery and verification completed. Docker loading is off unless you explicitly enabled it. |
| Client reports `loaded` | Import completed; check running containers separately. |
| Notifications are unavailable | The client still polls. Check the URL and whether the hub was started with `--events`. |
| An integrity or signature check fails | Check the pinned public key and publisher data. Do not bypass verification or reset sequence state to force acceptance. |
| Port 8080 is already in use | Stop the other example hub before starting its replacement. |

Stop native publisher, hub, and client processes with Ctrl-C. Preserve their directories for a restart. In particular, deleting publisher sequence state or client state is not an ordinary restart and loses continuity or cached downloads.

To remove the example hub container:

```sh
docker rm -f edgelab-hub
```

Its named state volume remains. Only if you want to discard that container's saved admin history after removing the container, run:

```sh
docker volume rm edgelab-hub-state
```

For Helm, back up any data you need before uninstalling: the chart manages both PVCs, and whether underlying storage survives depends on your storage reclaim policy.

```sh
helm uninstall edgelab-hub --namespace edgelab
```

Leave the origin, signing keys, publisher counter, and client cache intact unless you intend to discard the entire example. See [production gaps](PRODUCTION_GAPS.md) before planning unattended operation.
