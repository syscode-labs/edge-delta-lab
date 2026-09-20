# Packaged Linux installation proof

**PASS — 2026-09-20.** The retained terminal result is `PACKAGED_LINUX_PROOF_PASS` (exit 0); [result.json](result.json) records the measurements and lifecycle assertions. [transcript.txt](transcript.txt) retains the terminal completion excerpt, not a reconstructed execution log. The proof ran source `3939fac3d24b3b275401d386fba1985a0e4df02d`; the locally packaged amd64 bundle SHA-256 is `770bb27400239525a00e9f60f1fcfcf59e17d3f0f1c8ee843b9522b3c738e67c`.

## Measured result

| First loaded completion | Version 1 | Version 2 |
|---|---:|---:|
| Archive bytes | 3,653,120 | 3,653,120 |
| Downloaded chunks | 43 | 2 |
| Reused chunks | 0 | 41 |
| Chunk response body bytes | 3,635,917 | 300,931 |
| Integrity failures / retries | 0 / 0 | 0 / 0 |

Both versions ran with `docker run --rm --pull never --network none IMAGE_ID`, producing their expected payload hashes, including with the hub stopped. The publisher/hub restarted between versions. A receiver restart produced a **fresh** loaded completion with zero downloaded chunks, zero chunk requests and zero new `/chunks/` requests in the HTTPS proxy log (four other requests occurred). Status/restart/stop/start/uninstall passed; uninstall retained publisher keys/sequence and receiver public trust/state. This is import and offline payload execution, not application activation or health-gated rollout. Counts are chunk-body bytes, not total network traffic or a matched registry-pull comparison.

## Host/guest boundary

The macOS host built the release and controlled two **isolated Ubuntu 24.04 amd64 OrbStack LXC environments**, `edgelab-proof-hub` and `edgelab-proof-receiver`. Each had its own Docker daemon, image store and systemd service manager; daemon IDs and kernel identity are in the result. They share the OrbStack Linux VM kernel. This was neither a native macOS receiver nor two independent VM kernels.

Docker used `vfs` with `features.containerd-snapshotter=false` because nested overlay storage failed. OrbStack's global systemd drop-in disables several sandbox directives, including `NoNewPrivileges`, `ProtectSystem`, `ProtectHome` and private temporary directories; the complete effective unit/drop-in is retained in the result. **The stock unit's sandbox hardening is not validated by this execution.**

The password-protected Distribution `registry:2` used explicitly opted-in HTTP on the isolated guest network. The hub-to-receiver leg used nginx HTTPS with a locally issued CA securely installed in the receiver trust store, without TLS verification bypass. This proves Basic registry authentication and trusted HTTPS delivery, **not HTTPS registry transport in this live proof**. Only public signing trust and the public CA certificate crossed to the receiver. The fixture password and signing/TLS private keys stayed on the hub.

## Reproduction contract

Use fresh disposable Linux hosts, never a production Docker daemon. This is a bounded recipe, not an unattended provisioning script. The [installation guide](../../../deploy/compose/README.md) defines the supported operator interface; substitute your own registry/proxy endpoints. Public v0.1.0 publication remains pending until tag and successful release workflow.

1. From the recorded source, run `make release VERSION=v0.1.0` and verify `dist/SHA256SUMS`. Extract `edgelab-v0.1.0-linux-amd64.tar.gz` completely into `/opt/edgelab-bundle` on **both** hosts. No Go/compiler/source checkout is needed inside either host. Identical archive bytes require the same source and toolchain; [SOURCE_SHA256SUMS](SOURCE_SHA256SUMS) fingerprints all tracked non-evidence files in the integrated tree, not a claim that documentation was present during the historical run.
2. For the same host model, create only the two named machines with `orb create --isolated --user proof ubuntu:24.04 NAME`. Execute guest administration with `orb -m NAME -u root ...`. Install Ubuntu `docker.io`, `python3` and `curl` in both; additionally `docker-compose-v2`, `apache2-utils`, `nginx` and `openssl` on the hub. Enable each guest's own Docker daemon. Before image creation, configure `vfs` and disable the containerd snapshotter. Configure only the fixture registry address as an insecure registry on the hub Docker daemon.
3. Start Distribution with htpasswd Basic authentication, binding port 5000 and mounting a bcrypt password file read-only. Generate a new random password privately, use `docker login REGISTRY -u reader --password-stdin`, and confirm unauthenticated `/v2/` returns 401. Never retain the password, Docker login config or expanded Compose config in public evidence.
4. Configure a persistent nginx HTTPS proxy for `hub.proof.test` forwarding to `127.0.0.1:8080`. Issue a certificate with that DNS SAN; transfer its public CA to the receiver's OS trust store and provide name resolution. The original fixture certificate lifetime was two days: generate fresh certificates rather than reusing old ones.
5. On the hub run `/opt/edgelab-bundle/install hub setup --directory /opt/edge-delta-install --registry http://REGISTRY:5000 --repository proof/app --allow '^v' --username reader --password-file /root/registry-password`. Transfer **only** `keys/publisher.pub` over the trusted administration channel. On the receiver run `/opt/edgelab-bundle/install receiver setup --hub https://hub.proof.test --public-key /root/publisher.pub --device-id proof-receiver --docker-load`.
6. Build two real single-platform images with `FROM alpine:3.20`, `COPY payload /payload`, and `CMD ["sha256sum", "/payload"]`. For version `v` in `(1, 2)`, generate payload bytes with Python: `bytes(range(256))*16384 + f'version-{v}\n'.encode()*v`. Build with `docker build --no-cache`, then push immutable `proof/app:v1` and `proof/app:v2` separately. The base tag is not pinned; new builds can differ in image/archive hashes. The deterministic payload hashes in the result remain the content assertion.
7. For each version, wait for `phase=loaded` and the expected sequence. Capture the **first** completed JSON summary from `journalctl -u edgelab-receiver -o cat`, not a later no-op `summary.json`. Enumerate `docker images -a --no-trunc --format '{{.ID}}'` because imports can be untagged. Require exactly one image whose offline payload output matches that version's expected SHA-256. Restart publisher/hub after version 1 with `/opt/edge-delta-install/manage hub restart --directory /opt/edge-delta-install`; the same keys/state must deliver version 2.
8. Record the receiver summary modification time and proxy access-log line count, run `edgelab-manage receiver restart`, require a newer completed summary, and check zero downloaded chunks/chunk requests and no `/chunks/` paths in new access lines. Metadata/receipt traffic is allowed. Stop the hub and run both images offline again.
9. Exercise receiver stop/start with `systemctl is-active` assertions and hub stop/start/status through its manager. Uninstall both through their managers. Assert the receiver binary/unit are gone but `/etc/edgelab/publisher.pub` and `/var/lib/private/edgelab-receiver/summary.json` remain; assert publisher `keys/publisher.key` and `watcher-state/sequence` remain. Remove only the owned disposable machines after collecting sanitized evidence. No global Docker prune.

## Failure ledger

- Initial runtime assembly failed with nested overlay storage. Switching these disposable daemons to `vfs`/classic storage resolved the environmental blocker; no product source change was needed.
- The initial fixture passed `--provenance=false` to Ubuntu's legacy Docker builder, which does not support it. Removed that harness-only flag.
- The first delivery harness expected a pre-digest release name; the product correctly emitted digest-qualified immutable names. That harness run was stopped, detection changed to loaded phase plus expected sequence, and existing pushed images were reused rather than overwritten.
- An offline enumeration assertion saw no images because it omitted `docker images -a`; loaded images were untagged. Including all images and verifying by actual offline payload fixed the assertion.
- Repository-not-yet-created 404s caused documented watcher backoff during setup. The publisher was restarted during investigation; the successful run is not an uninterrupted recovery-time benchmark.
- Final corrected delivery/lifecycle execution exited zero. Closeout did **not** repeat the long proof: it checked the retained result against the terminal completion, harness assertions and freshly rebuilt identical bundle, then ran the exclusive regression gates.

## Retention and limits

Retained: result JSON (including effective systemd settings), original terminal completion excerpt, source checksums and reproduction/failure contract. Raw orchestration logs, ignored harnesses, private keys/passwords, guest addresses, Docker archives/cache layers and disposable machines are not retained. This small evidence set audits the recorded outcome and source identity; it cannot independently replay archive verification without regenerating fixtures.

[VALIDATION.md](VALIDATION.md) records final regression and cleanup checks. Public release publication, arm64 runtime, independent-kernel/stock-systemd sandbox execution, reboot/power-loss recovery, WAN loss/latency behavior, retention/GC and production readiness remain unproved. HTTPS proxy/certificate issuance and renewal remain external prerequisites.
