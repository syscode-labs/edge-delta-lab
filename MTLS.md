# Optional mutual-TLS front door

The hub remains plain HTTP on **127.0.0.1 only**. This separate Caddy container terminates HTTPS and requires a verified client certificate for every request, including health, manifests, objects and receipts. Removing it does not change hub code, publisher keys, data or the receiver protocol. This is transport authentication, not per-device authorization or a replacement for signed manifests.

## Requirements

First install the publisher/hub using [OPERATIONS.md](OPERATIONS.md). Keep its `hub.env` and work from the same source tree or extracted Linux release directory. The wrapper needs Linux Docker with Compose v2, Python 3, Make and OpenSSL 1.1.1 or newer. Host networking reaches the hub's loopback port; this is not a portable Docker Desktop networking recipe. Receiver mTLS uses systemd `LoadCredential` (systemd 247 or newer); no inbound receiver port is needed.

## Initialize the wrapper

```sh
# Edit hub.env: writable MTLS_DIRECTORY (~/ is supported),
# MTLS_HOST matching DNS, and MTLS_PORT distinct from HUB_PORT.
make mtls-init
make mtls-client CLIENT=edge-01
make mtls-up
```

Use an unprivileged operator with Docker access (root-equivalent). For a custom directory, assign its **parent** to that operator, not MTLS_DIRECTORY itself: initialization refuses existing paths, including symlinks, rather than replacing trust. Only directory paths expand `~`, never shell expressions or secrets. Permit inbound TCP on MTLS_PORT (default 8443), not HUB_PORT. No HTTP redirect listener is created. DNS must resolve MTLS_HOST to the Linux host; the generated server certificate covers that exact name/IP.

Initialization creates **two independent CAs**: server trust and client enrollment. Server and client leaf certificates have only their respective extended key usages. CA certificates expire in 365 days and leaf certificates in 90 days. There is no automatic renewal or per-client revocation: plan rotation before expiry; replace the trusted client CA to revoke its entire cohort. Certificate identity is not bound to a receiver's device-id. Caddy mounts no authority private keys.

## Enroll a receiver

Transfer only `MTLS_DIRECTORY/clients/edge-01/{hub-ca.pem,client.pem,client.key}` to edge-01 through a trusted channel, plus the publisher's **public** key. Never transfer `publisher.key`, `server.key`, `authority/` or another client's private key. Protect enrollment directories with mode `0700` and private keys with `0600`. Back up CA private keys securely/offline; they are needed only to issue certificates.

Prepare a fresh receiver's `receiver.env` as described in [receiver installation](OPERATIONS.md#receiver-installation-and-trust-transfer). Set `HUB_URL=https://the-configured-host:8443` and the absolute enrollment paths:

```dotenv
HUB_CA=/etc/edgelab-enrollment/hub-ca.pem
HUB_CLIENT_CERT=/etc/edgelab-enrollment/client.pem
HUB_CLIENT_KEY=/etc/edgelab-enrollment/client.key
```

Then use the normal receiver installation:

```sh
sudo make receiver-up
sudo make receiver-status
```

The installer copies enrollment into root-private `/etc/edgelab/tls`. Systemd delivers credentials to the DynamicUser service through its private credential directory. Because its read-only key can be `0440`, the launcher copies only that key to a new `0600` file in the service-owned `0700` `/run/edgelab-receiver` RuntimeDirectory. It checks ownership, mode and symlinks, then atomically replaces the key without following a destination symlink. Systemd removes the ephemeral copy on stop; root-private enrollment and read-only credentials stay unchanged. The general TLS client's owner-only key check is **not** relaxed, and credential values never become command-line arguments.

Direct CLI callers use `--hub-ca`, `--hub-client-cert` and `--hub-client-key` with those three enrollment files (key mode `0600` or stricter).

## Persistence, removal and replacement

Use [the base lifecycle guide](OPERATIONS.md#lifecycle) for publisher/receiver restart, uninstall, retained configuration and deliberate re-enrollment. Editing env files does not replace existing enrollment. Wrapper lifecycle is independent:

```sh
make mtls-down                 # removes only proxy containers/network
make hub-status                # hub remains available on loopback
make mtls-up                   # validate and recreate, same keys
```

After `mtls-down`, remote HTTPS must fail until a replacement proxy starts. Point the replacement at `127.0.0.1:HUB_PORT`, require and verify client certificates, and configure compatible server/client trust. Never expose the hub HTTP port publicly. Remove MTLS_DIRECTORY only when its trust material is backed up or intentionally retired; do not run broad Docker cleanup.

To use publicly trusted HTTPS without client certificates, empty `HUB_CA`, `HUB_CLIENT_CERT` and `HUB_CLIENT_KEY` before a **fresh** receiver install. An existing enrollment needs the deliberate retained-config procedure in Operations, not just an env edit.

## Caddy configuration

The shipped Caddyfile uses `client_auth { mode require_and_verify; trust_pool file { pem_file ... } }`; see the official [TLS directive reference](https://caddyserver.com/docs/caddyfile/directives/tls). The image is pinned to `caddy:2.10.2-alpine` (a version tag, not an immutable digest). `mtls-up` runs `caddy validate` before starting. The admin API is disabled, no access log is enabled, and the container mounts only runtime server identity and the public client CA. Registry credentials and publisher signing keys stay outside it.

[TESTING.md](TESTING.md) owns installer/Caddy evidence, acceptance boundaries and reproduction commands.
