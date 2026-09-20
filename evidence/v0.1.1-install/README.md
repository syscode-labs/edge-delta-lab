# v0.1.1 installer / optional proxy validation

Executed locally on the OrbStack Linux Docker daemon from branch usability-install.
Only owned disposable containers were used; no cluster, remote release, or image
was published. Initial and final Docker inventory had no containers or volumes;
the pre-existing `kind` network was left untouched.

Commands executed successfully:

```sh
make check
make install-contract
helm lint deploy/helm/edgelab-hub
make release VERSION=v0.1.1
(cd dist && shasum -a 256 -c SHA256SUMS)
python3 scripts/package_smoke.py --archive dist/edgelab-v0.1.1-linux-arm64.tar.gz
python3 scripts/mtls_smoke.py --work /tmp/edgelab-mtls-proof-final
```

- `make check`: Go vet, Go tests, Python contracts, Compose contracts, Go race tests.
- Final installer contracts: 5 lifecycle/enrollment, 9 installer, 6 packaging and
  3 Compose tests; all passed. Systemd calls in installer contracts are mocked.
- Release: real Go builds for linux/amd64, linux/arm64 and darwin/arm64; chart
  0.1.1; all four archive checksums verified. Local dist files are not published
  and must be rebuilt after the independent Go/docs changes are integrated.
- Extracted Linux arm64 archive: native keygen, installer help, Make target
  dispatch, mTLS init/client enrollment and OpenSSL verification passed inside
  an owned Linux container with no Go, source checkout, or mounted Docker socket.
- `mtls-result.json`: real Caddy allowed client, absent/wrong-CA denial, restart,
  removal preserving loopback backend, independent replacement and recreation.
  Image digest is retained; runtime pins the version tag rather than the digest.

The first Caddy probe exposed the official binary's NET_BIND_SERVICE file
capability: dropping the complete bounding set caused exec EPERM. The wrapper
now drops all capabilities and adds back only NET_BIND_SERVICE; the final full
probe passed with no-new-privileges, readonly root, user UID/GID and runtime-only
certificate mount.

Not proven here: live systemd installation/reboot, Go client mTLS (parallel API
work not yet merged), remote-host DNS/firewall access, full registry-to-Docker
receiver delivery through TLS, production certificate rotation/revocation, or
container activation/health. The HTTP backend in the TLS boundary harness is an
owned test server, not a claim of full artifact delivery.
