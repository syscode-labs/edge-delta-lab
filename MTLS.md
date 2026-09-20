# Optional mutual-TLS front door

The hub remains plain HTTP on **127.0.0.1 only**. This separate Caddy container
terminates HTTPS and requires a verified client certificate before forwarding any
request, including health, manifests, objects and receipts. Removing it does not
change hub code, publisher keys, data, or the receiver protocol. Another proxy can
replace it. This is transport authentication, not per-device authorization or a
replacement for signed manifests.

## Requirements

Use a Linux Docker host with Compose v2, Python 3, Make and OpenSSL 1.1.1 or newer.
The wrapper uses host networking to reach the hub's loopback port. It is not a
portable Docker Desktop networking recipe. Source installs additionally need Go;
extracted Linux bundles do not. Receivers require Linux systemd; mTLS uses
`LoadCredential` (systemd 247 or newer). No inbound receiver port is needed.

## Install

From the source tree or extracted Linux release directory:

```sh
cp hub.env.example hub.env
# Edit hub.env: absolute writable HUB_DIRECTORY and MTLS_DIRECTORY, registry,
# repository, allow regex, MTLS_HOST matching DNS, and distinct ports.
# REGISTRY_PASSWORD_FILE points to a chmod-600 file, never a password argument.
make hub-up
make mtls-init
make mtls-client CLIENT=edge-01
make mtls-up
make hub-status
```

Use an unprivileged operator with Docker access and writable installation paths.
Docker access is root-equivalent. The example `/opt` paths may need to be created
or assigned by an administrator. Permit inbound TCP on MTLS_PORT (default 8443),
not HUB_PORT. No HTTP redirect listener is created. DNS must resolve MTLS_HOST to
the Linux host; the generated server certificate covers that exact name/IP.

The initializer refuses existing paths rather than silently replacing trust. It
creates **two independent CAs**: server trust and client enrollment. Server and
client leaf certificates have only their respective EKUs. CA certificates expire
in 365 days and leaf certificates in 90 days. There is no automatic renewal or
per-client revocation: plan rotation before expiry; replace the trusted client CA
to revoke its entire cohort. Do not claim certificate identity is bound to a
receiver's device-id. Caddy has no authority private-key mounts.

Only transfer `MTLS_DIRECTORY/clients/edge-01/{hub-ca.pem,client.pem,client.key}`
to edge-01 through a trusted channel, plus **publisher.pub** from the hub. Never
transfer publisher.key, server.key, authority/, or another client's private key.
Protect enrollment directories with mode 0700 and private keys with 0600.
Back up CA private keys securely/offline; they are needed only to issue certificates.

On the receiver, set absolute paths in receiver.env:

```sh
cp receiver.env.example receiver.env
# Set HUB_URL=https://the-configured-host:8443, DEVICE_ID, public key and TLS paths.
sudo make receiver-up
sudo make receiver-status
sudo make receiver-restart
sudo make receiver-stop
sudo make receiver-up
```

The installer copies TLS enrollment into root-private `/etc/edgelab/tls`.
Systemd delivers credentials to the DynamicUser service via its private credential
directory; private keys are not world-readable and values are never command-line
arguments. Direct CLI callers use `--hub-ca`, `--hub-client-cert`, and
`--hub-client-key` with those same three files.

## Persistence, removal and replacement

`hub-up` installs once, then starts retained Compose services. `hub-stop` stops
without deletion; `hub-restart` restarts. `hub-uninstall` removes containers/network,
not keys, registry credentials, configuration or state. A later `hub-up` reuses
the retained directory. Changing hub.env does not rewrite an existing installation.

`receiver-uninstall` disables/removes the service and installed binaries, retaining
`/etc/edgelab` and `/var/lib/edgelab-receiver`. A later `receiver-up` reinstalls using
retained trust/configuration. Changing receiver.env does not rewrite retained
configuration. To deliberately reset enrollment, first uninstall and explicitly
archive/remove `/etc/edgelab`; preserve verified state unless a reset is intended.

```sh
make mtls-down                 # removes only proxy containers/network
make hub-status                # hub remains available on loopback
make mtls-up                   # validate and recreate, same keys
```

After `mtls-down`, remote HTTPS must fail until a replacement proxy is started.
Point that proxy at `127.0.0.1:HUB_PORT`, require and verify client certificates,
and configure compatible server/client trust. Never solve replacement by exposing
the hub HTTP port publicly. Remove MTLS_DIRECTORY explicitly only when its trust
material is backed up or intentionally retired. Do not run broad Docker cleanup.

To use publicly trusted HTTPS without client certificates, empty all three HUB_*
TLS file fields before a **fresh** receiver install. HTTP is rejected unless
ALLOW_HTTP=true is explicitly set; use it only in isolated tests.

## Caddy configuration source

The shipped Caddyfile uses `client_auth { mode require_and_verify; trust_pool file
{ pem_file ... } }`, checked against the official
[TLS directive reference](https://caddyserver.com/docs/caddyfile/directives/tls).
The image is pinned to `caddy:2.10.2-alpine` (version tag, not immutable digest).
`mtls-up` runs `caddy validate` before starting. Admin API is disabled, no access
log is enabled, and the container mounts only runtime server identity and the
public client CA. Registry credentials and publisher signing keys stay outside it.
