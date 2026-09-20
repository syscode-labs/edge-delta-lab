# Persistent publisher, hub and Linux receiver

The [README](../../README.md) is the short packaged path. This guide covers its prerequisites, trust boundary and lifecycle. Registry v2 → publisher → hub → receiver works without a specific registry vendor, VPN, Helm or Grafana. Setup starts managed services, not foreground terminals.

## Install from the Linux bundle

See the [v0.1.1 asset names and download commands](../../README.md#install), or obtain a locally built candidate bundle from a maintainer. Extract the complete Linux archive on each host. Requirements: Python 3, make, local Docker Engine; Compose on the publisher host; systemd and sudo on the receiver. Import requires a `docker` group able to access its local daemon socket.

The simplest interface is `cp hub.env.example hub.env`, edit the values, then `make hub-up`; on the receiver use `cp receiver.env.example receiver.env`, edit, then `sudo make receiver-up`. Keep these env files private. Status/restart/stop/uninstall are `make hub-status`, `make hub-restart`, `make hub-stop`, `make hub-uninstall` and their `sudo make receiver-*` counterparts. These wrappers call the same manager described below; do not run both setup paths on an existing installation.

From the extracted bundle on the publisher host:

```sh
./install hub setup --directory "$HOME/edge-delta-install" \
  --registry https://registry.example.net --repository team/app --allow '^v'
"$HOME/edge-delta-install/manage" hub status --directory "$HOME/edge-delta-install"
```

Use immutable single-platform tags matching receiver architecture. Multi-platform indexes are unsupported. `--allow` is a Go/RE2 regex, not a semantic-version selector; multiple eligible tags are processed in lexicographic poll order. Start with a narrow filter. The example does not create a registry or push an image.

Setup creates a new signing identity/state, assembles a runtime container from the **bundled binary** plus Alpine/CA certificates, and starts both services. There is no Go build or source dependency. Registry/base-image/package network access is required. Setup refuses existing paths (including symlinks). A failed setup can leave partial files: inspect and remove only that fresh failed installation before retrying. Never delete a live installation to overcome this check.

Install on the Docker daemon's local host; remote contexts cannot copy these bind mounts. Rootless/user-namespace-remapped Docker and SELinux labeling are not configured by the installer. `--port 18080` changes the loopback hub port. The template `compose.yaml` in this source tree is not a ready installation.

### Registry Basic authentication and retry

For a private registry, have your normal credential manager place the password in a private local file, then add these flags to the setup command:

```sh
--username YOUR_USER --password-file /path/to/private-password-file
```

Do not put a password in a URL or shell argument. Setup copies it to `registry-password` (`0600`) inside the private installation. The manager injects it into **only the publisher** on all lifecycle operations. Registry tag listing, manifest discovery and image export use the same Basic credentials. Empty/missing passwords fail closed; never publish `docker inspect` or expanded `docker compose config` output containing environment secrets. Docker administrators can read them.

Use HTTPS with trusted certificates. An explicit `--registry http://...` selects unencrypted registry access and emits a warning; this is an opt-in for a trusted isolated test network, not a recommended production setting. No blanket TLS-verification bypass is installed.

A digest becomes seen only after successful export, signing and channel promotion. Failed publications are retried on subsequent polls with exponential error backoff (30-second initial interval, capped at ten minutes); a failed channel promotion can resume an already-created immutable release. An initially nonexistent repository also triggers this backoff. State-save errors are returned rather than silently accepted. This is not an exactly-once transactional queue. Release identity includes repository, tag and digest, so a moved tag creates a distinct immutable release. Prefer **new version tags** for an auditable workflow. Retrying an older publication never rolls the channel backward; moving a tag back to already-published content is not a rollback command. Do not reset sequence/trust state to force a retry. Inspect logs for failures:

```sh
# Logs do not require following a foreground process indefinitely.
# The manager is preferred for lifecycle because it loads stored credentials.
REGISTRY_PASSWORD=unused docker compose -f "$HOME/edge-delta-install/compose.yaml" logs --tail 50 publisher hub
curl --fail http://127.0.0.1:8080/releases/desired.json
```

The dummy variable above only satisfies Compose interpolation for a **read-only logs command**; never use it with up/recreate. HTTP 404 before first publication is expected. Service status or HTTP health alone is not proof of a signed publication.

## TLS and network boundaries

The hub is unauthenticated HTTP bound to `127.0.0.1:8080`. Keep it there. On the same host, configure your existing persistent reverse proxy to provide `https://hub.example.net` with a trusted certificate. A minimal nginx server block is:

```nginx
server {
    listen 443 ssl;
    server_name hub.example.net;
    ssl_certificate /etc/ssl/hub/fullchain.pem;
    ssl_certificate_key /etc/ssl/hub/private.key;
    location / {
        proxy_pass http://127.0.0.1:8080;
    }
}
```

Certificate issuance, renewal, firewall/network access and proxy service management are operator prerequisites, not features of `./install`. Alternatively, use the separately managed [Caddy mTLS wrapper](../../MTLS.md): `make mtls-init`, `make mtls-up`, `make mtls-client`, and `make mtls-down`. It is removable without changing the hub or release-signing keys; follow that guide for certificate distribution and renewal. The installed receiver polls; the nginx block above does not enable WebSocket upgrades for optional push clients. A private CA must be securely installed in the receiver's normal OS trust store. Never turn off certificate verification.

TLS encrypts and authenticates the server; it does **not** authenticate receivers. Restrict access to your trusted network or supply a compatible external access boundary. This installer has no HTTP Basic/bearer client-auth flags. For isolated testing or an independently secured persistent tunnel only, use `--hub http://... --allow-http`. A foreground SSH session is not the managed installation path.

## Receiver installation and trust transfer

Copy **only** `keys/publisher.pub` from the publisher installation over verified SSH or another trusted channel. Verify the host identity independently; downloading this key from the unauthenticated hub would not establish trust. Never transfer `publisher.key`, registry credentials, or the entire installation. The receiver config contains only the hub URL, public key, device ID and opt-ins.

From the Linux bundle on the receiver:

```sh
sudo ./install receiver setup --hub https://hub.example.net \
  --public-key /path/to/publisher.pub --device-id receiver-01 --docker-load
sudo edgelab-manage receiver status
sudo journalctl -u edgelab-receiver -n 50 --no-pager
```

The installer validates the URL/public key and refuses to overwrite existing binaries or an installation. It installs `edgelab` and `edgelab-manage` under `/usr/local/bin`, config/public key under `/etc/edgelab`, and enables `edgelab-receiver.service`. A systemd DynamicUser owns `/var/lib/edgelab-receiver`; state persists between runs. Docker group access is root-equivalent and enabled **only** by `--docker-load`. Omit it to stage/verify without importing. The receiver needs no registry credentials or inbound listener.

Wait for `phase: loaded` (or `staged` without import), then push the next immutable version. Read `summary.json` in the state directory with sudo; first per-release summaries show actual transfer/reuse, later reconciliations can overwrite them with zero-download checks. `loaded` proves verified import, not container activation, application health or continued Docker inventory.

## Lifecycle

Publisher host:

```sh
"$HOME/edge-delta-install/manage" hub status --directory "$HOME/edge-delta-install"
"$HOME/edge-delta-install/manage" hub restart --directory "$HOME/edge-delta-install"
"$HOME/edge-delta-install/manage" hub stop --directory "$HOME/edge-delta-install"
"$HOME/edge-delta-install/manage" hub start --directory "$HOME/edge-delta-install"
"$HOME/edge-delta-install/manage" hub uninstall --directory "$HOME/edge-delta-install"
```

Receiver:

```sh
sudo edgelab-manage receiver status
sudo edgelab-manage receiver restart
sudo edgelab-manage receiver stop
sudo edgelab-manage receiver start
sudo edgelab-manage receiver uninstall
```

Hub uninstall removes containers/network, retaining the installation directory, manager and runtime files; `hub start` can resume. Receiver uninstall disables/removes its unit and binaries but retains `/etc/edgelab` and systemd state (which may reside under `/var/lib/private/edgelab-receiver`). **Setup refuses retained config**, so uninstall is not an in-place upgrade/reinstall command. Back up retained config/state before an explicit fresh reset; this version has no automatic receiver upgrade/restore workflow. Do not erase retained anti-rollback state merely to force a retry. Use stop/start for temporary shutdowns.

For a full purge, stop/uninstall first, archive trust/history, then explicitly remove only the installation/config/state you intend to discard. The installed images in Docker are not removed by receiver uninstall. Never use global Docker prune as application cleanup.

## Persistent data and permissions

Keep one publisher and one receiver per state directory. Back up the complete publisher installation consistently **while stopped**, including:

- `origin/`: publisher-writable, hub read-only; published releases/chunks.
- `watcher-state/`: digest history, monotonic sequence and temporary archive exports. Abrupt termination may leave scratch under `tmp`; clean only stale scratch with publisher stopped.
- `keys/publisher.key`: private signing identity, mounted only into publisher.
- `keys/publisher.pub`: public trust key distributed independently.
- `hub-state/`: event log/admin socket.
- `watcher.yaml`, `compose.yaml`, `registry-password` when configured, runtime assets and `manage`.

The installation/key directories are `0700`; containers run as UID 100 and the setup user's primary GID. State directories are `0770`, signing key/config `0640`; only trusted users should share that group. Both containers have read-only root filesystems, dropped capabilities and `no-new-privileges`. Neither mounts the Docker socket. Archive reconstruction and Docker storage need additional space; automatic retention/GC and key rotation are not implemented.

Edit repository selection in `watcher.yaml`, then restart; do not regenerate keys or sequence counters for updates. Preserve owner/group permissions on restore. Receiver cache/import markers and monotonic state must likewise survive restart. Markers record previous import, not current inventory.

## Source build alternative

Developers with Go matching `go.mod` may use the existing checkout-based setup:

```sh
go build -o bin/edgelab ./cmd/edgelab
python3 deploy/compose/setup.py --directory "$HOME/edge-delta-source" \
  --registry https://registry.example.net --repository team/app --allow '^v'
docker compose -f "$HOME/edge-delta-source/compose.yaml" up -d --build
```

This alternative builds the checkout's daemon target and needs the checkout at its original location for rebuilds. With `--username`, export `REGISTRY_PASSWORD` for each Compose invocation; unlike the packaged manager this source setup does not persist a password file. Use Compose `ps`, `restart`, `stop`, `up -d`, and `down` for its lifecycle. Native foreground/server and optional Helm instructions are in the [runtime guide](../../docs/DOCKER_RUN.md).

## Validation

[TESTING.md](../../TESTING.md) owns the current test commands, retained proof and pending acceptance. Historical installer execution is not proof that a new bundle or mTLS wrapper has run.
