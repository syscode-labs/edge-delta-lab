# Publisher + hub on one Docker host

This is the intended-use source installation, not the synthetic root Compose demo.
The publisher watches **your existing registry repository**, exports eligible images,
signs/chunks them into a persistent origin, and promotes the `desired` channel.
The hub serves that same origin read-only. Neither container has the Docker socket.
Native receivers remain separate and explicitly opt into `watch --docker-load`.

## Install

Requirements: Python 3, Go matching `go.mod`, Docker Engine/Compose, and network
access from Docker containers to the registry and Dockerfile build dependencies.
Run from this checkout **on the Docker daemon host** (Docker Desktop works too).
Remote Docker contexts do not copy bind-mounted host files; install on that host
instead. Rootless/user-namespace-remapped daemons and SELinux bind-mount labeling
may need host-specific ownership/label adjustments; they are not configured here.

```sh
go build -o bin/edgelab ./cmd/edgelab
python3 deploy/compose/setup.py \
  --directory "$HOME/edge-delta-install" \
  --registry https://registry.example.net \
  --repository team/app \
  --allow '^v'
docker compose -f "$HOME/edge-delta-install/compose.yaml" up -d --build
docker compose -f "$HOME/edge-delta-install/compose.yaml" logs -f publisher hub
```

Replace the registry/repository with ones you actually operate or can read; this
example does not supply an image, push one, or create a registry. Prefer an HTTPS
registry with publicly trusted certificates and immutable, single-platform image
tags. Container `localhost` is not the host. Tag filters use Go/RE2 syntax; malformed
regexes are rejected by `watch-registry` at startup (check publisher logs).
Use a narrow allow filter: this is not a semantic-version resolver; multiple
eligible tags may each be published/promoted in lexicographic poll order.

Setup refuses **any existing destination**, including an empty directory. It
creates real keys with the built native `edgelab keygen`, never placeholder keys.
Use `--binary /absolute/path/to/edgelab` to select another built native binary;
`--port 18080` changes the host loopback port. It does not contact the registry
or prove registry access. A setup failure can leave a partial directory: inspect
and remove only that fresh partial installation before retrying, never a live one.

Both services build the checkout's `Dockerfile` **daemon** target; no published
Edge Delta image is assumed. Keep the checkout at its original path for rebuilds.
The checked-in `compose.yaml` is the setup template, not a ready installation.
Generated `.yaml` files use JSON syntax (a YAML subset) to preserve regexes safely.

For username/password registry authentication, add `--username YOUR_USER` to setup
and export `REGISTRY_PASSWORD` in the shell that invokes Compose. Compose fails
closed if it is missing. The password goes only to the publisher environment,
not to the hub or generated files. Container inspection/Compose config output can
expose environment credentials to Docker administrators; do not share that output.

## Persistent data and permissions

The generated directory contains:

- `origin/`: publisher-writable; hub mounts it **read-only**.
- `watcher-state/`: durable digest state, monotonic sequence, and disk-backed
  temporary archive exports. Only the publisher mounts it; allow room for full
  uncompressed archives as well as the chunk origin. Abrupt termination can leave
  scratch directories under `watcher-state/tmp`; clean stale scratch only while
  publisher is stopped, never the digest/sequence files.
- `hub-state/`: hub event log and admin socket, not publisher data.
- `keys/publisher.key`: mounted read-only into **publisher only**.
- `keys/publisher.pub`: distribute this trust key to receivers over a trusted path.
- `watcher.yaml` and `compose.yaml`: repository selection and local deployment.

Containers run as **UID 100**, with the setup user's primary host GID. State
folders are group-writable (`0770`); the private key and config are group-readable
(`0640`). The installation and keys directories are host-private (`0700`). This
avoids sudo/chown and world-writable directories on ordinary Linux bind mounts.
Only trusted users should belong to that host group. Docker administrators already
have host-level access. Both containers have read-only root filesystems, no Linux
capabilities, and `no-new-privileges`. Do not scale publisher above one instance:
its sequence and origin are a single-writer installation.

## Check and connect

The default endpoint is `http://127.0.0.1:8080`. Before the first successful
publication, `/releases/desired.json` can return 404; that is not proof the hub
failed. Wait for `release-published` in publisher logs, then:

```sh
curl --fail http://127.0.0.1:8080/releases/desired.json
```

The hub is **unauthenticated HTTP**, intentionally bound to loopback. Do not change
it to a public bind and call it secure. For remote receivers, supply a trusted
private tunnel or a separately configured authenticated TLS proxy; neither is
installed here. HTTP receivers must explicitly pass `--allow-http`. A receiver
pins `keys/publisher.pub` and watches `/releases/desired.json`. Docker loading is
explicit and privileged; a successful load does not start or health-check an app.

## Lifecycle

```sh
# Inspect / restart without changing keys or publisher history.
docker compose -f "$HOME/edge-delta-install/compose.yaml" ps
docker compose -f "$HOME/edge-delta-install/compose.yaml" restart

# Stop and remove containers/networks; bind-mounted data remains.
docker compose -f "$HOME/edge-delta-install/compose.yaml" down

# Resume, or rebuild after reviewing source changes in the original checkout.
docker compose -f "$HOME/edge-delta-install/compose.yaml" up -d --build
```

The watcher currently records a seen digest before publication completes. A failed
export/publication is not automatically retried for that unchanged digest. Check
publisher logs and the served channel after every update; do not reset sequence or
trust state to force a retry. This is an experimental publisher, not a durable
transactional release queue.

Edit `watcher.yaml` then restart publisher to change selection/polling. Do not
replace the signing key or reset the sequence to perform an update. Back up the
**whole installation consistently while stopped**, including private key, origin,
and watcher history/sequence. Restore together and retain the UID/GID access
rules. A fresh setup is a new trust identity, not a repair of an existing one.

To uninstall, run `down`, archive the complete directory, and only then explicitly
remove the installation directory if its keys/history/artifacts are no longer
needed. `down -v` does not erase these bind-mounted files. The lab provides no
automatic retention/GC or production key rotation; monitor disk usage.

## Packaging tests

```sh
python3 deploy/compose/test_setup.py -v
```

These tests build the real native binary, generate real keys, check refusal to
overwrite, mounts, permissions, authentication separation and (when Docker CLI is
available) parse the result with `docker compose config --quiet`. They do not claim
registry publication, running-container permissions, or receiver Docker-load
end-to-end proof; those require a real registry and daemon execution.
