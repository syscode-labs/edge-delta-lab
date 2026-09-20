# Real intended-use installation proof

## Result

**PASS** — `python3 scripts/install_smoke.py --work work/install-proof-5` exited 0 on 2026-09-20. This run uses the same generated Compose installation as the README, with only a disposable real Distribution `registry:2` service added for testing. It does not use a fake registry, synthetic archive, or mocked Docker importer.

Topology: native macOS/amd64 client talking to a local Linux/amd64 Docker 29.4.0 daemon (OrbStack); publisher, hub and test registry run in Docker. Source build and receiving import use the **same daemon**. Source image references are removed and absence checked before each import. The proof binds the loaded store reference to the signed config digest by saving its bytes; Docker 29's manifest ID is not confused with the image config ID.

| Measured first completed transfer | Cold v1 | Changed v2 |
|---|---:|---:|
| Chunk-response-body bytes | 7,834,748 | 2,459,261 |
| Downloaded chunks | 102 | 27 |
| Reused chunks | 0 | 73 |
| Integrity failures | 0 | 0 |

Both releases imported through one persistent native `watch --docker-load` process. Each loaded image ran with `--pull never --network none`, returning the expected release marker and payload SHA-256. The images contain Alpine and a 4 MiB payload; v2 changes 32 KiB plus the release marker. Registry exports include compressed layer bytes, so these numbers are not an idealized per-uncompressed-layer delta benchmark.

Publisher and hub were restarted **before v2 was built/pushed**, and the existing installation successfully published v2 at sequence 2. A further restart preserved the signing key and sequence. The native client was then restarted with its same state; the gate requires a **new post-restart summary write**, not stale evidence. It reported `loaded`, zero new chunk downloads, and 100 reused chunks.

## Reproduce

From the checkout on a compatible local Docker host, install Go, Python 3, Docker Compose and BuildKit-capable Docker. Internet access is needed for Dockerfile dependencies, `registry:2`, and `alpine:3.20`.

```sh
python3 deploy/compose/test_setup.py -v
make check
python3 scripts/install_smoke.py --work work/my-fresh-install-proof
```

Use a fresh work directory each time. The opt-in harness creates a uniquely named Compose project, loopback-only ports, keys, real images and client state. It removes owned containers, registry volumes and proof image references afterward. It retains installation state and private test keys under ignored `work/`; **never publish that directory wholesale**. Downloaded dependency images/build cache may remain. No cluster or existing service is modified.

## Retained files

- `result.json`, `v1-summary.json`, `v2-summary.json`: checked outcomes and first-transfer counters.
- `transcript.log`: actual build, registry push, image absence, stored-config verification, offline execution, restart and teardown commands/output.
- `client.log`: actual persistent client events and summaries.
- `cleanup.json`: no cleanup exceptions; a subsequent `docker ps --filter name=edinstall-` returned no containers.
- `check.log`: passing Go vet, Go tests/race tests and existing Python suite. The three new setup tests also passed separately; OpenSpec structural check passed.

Checkout paths are replaced by `<checkout>`. These logs contain only anonymous disposable registry traffic and public image references. No private signing key, auth token, generated installation config, or cache/archive is retained here.

## Issues found and scope limits

The first real registry attempt exposed a publisher bug: an explicit `http://registry:5000` was changed into HTTPS when exporting the image. `imageReference` now preserves explicit HTTP; regression tests cover HTTP versus HTTPS on a non-loopback hostname. HTTPS remains the intended external-registry default.

Two earlier harness attempts were corrected: Docker 29 needs stored-reference/config-digest binding rather than treating source inspect IDs as loaded IDs; mixed client output includes a Docker load status line, so the parser must skip non-JSON text and retain the **first** transfer summary. Review also tightened the restart gate against stale summaries and made cleanup steps independent.

**Not proved:** distinct remote receiver or SSH tunnel execution, authenticated private registry, production TLS/auth proxy, arm64 execution, reboot/systemd receiver supervision, activation/health-gated rollout, hardware power-loss durability, or production readiness. This is a bounded real installation integration test, not a production deployment certification.
