# Closeout validation

2026-09-20; runtime source remains `3939fac3d24b3b275401d386fba1985a0e4df02d`. This integration changes documentation/evidence only. No push or tag.

| Gate | Result |
|---|---|
| `make install-contract` | PASS: 5 installer, 6 packaging and 3 setup tests; launcher shell syntax |
| `go test -count=1 ./internal/registry` | PASS: authenticated discovery/export and publication/retry package |
| `make -j1 check` (exclusive, no competing suite) | PASS: Go vet, all Go tests, 56 Python tests, 3 setup tests, all Go race tests |
| `make openspec-check` | PASS: 42 requirements, 3 editable scenes |
| `openspec validate --all --strict --no-interactive` (CLI 1.6.0) | PASS: delta-transport spec and both changes (docker-e2e-sender-observability, registry-watcher-hub-spoke), 3/3 |
| `make release VERSION=v0.1.0` and `shasum -a 256 -c SHA256SUMS` in dist | PASS: all four archives; rebuilt amd64 hash exactly matches historical proof |
| Extracted consumer bundle checks | PASS: exact seven members/modes, five installer assets equal current and historical source, launcher runs, all six README installer/manager commands parse without mutating the host |
| Evidence consistency | PASS: both payload hashes recomputed from fixture recipe; loaded/integrity/restart assertions checked; 137 tracked non-evidence source files fingerprinted |
| Documentation/privacy checks | PASS: relative links and heading anchors, all shell-fence syntax, Gitleaks with redacted output (no leaks), home-path/private-address/key/kubeconfig/infrastructure-pattern scan of all changed deliverables |
| `git diff --check` | PASS |

The first closeout link check caught an over-deep relative link in this new evidence README; corrected and the same gate reran successfully. No product defect or runtime-source fix was required.

The retained live terminal completion was cross-checked against the original worker transcript and final harness assertions before removing the ignored harnesses. This is inherited live proof plus fresh regression/package validation, not a second live lifecycle run.

## Owned-resource cleanup

Before deletion, readback confirmed the two guest Docker IDs matched `result.json`, both used `vfs`, receiver binaries/unit were absent while public trust/state remained, and publisher trust/sequence remained. The hub still ran only the owned registry container plus the external nginx service. Product uninstall had already removed publisher/hub containers.

Executed `orb delete --force edgelab-proof-hub edgelab-proof-receiver`. Both isolated machine filesystems, private fixture credentials, nginx and their own Docker resources were removed with them. Subsequent `orb list` was empty; host `docker ps -a` was empty; process inspection found no running packaged-proof/proof-deliver harness. Removed only `work/packaged-proof.py`, `work/proof-deliver.py` and the packaged-proof bytecode caches, after extracting the reproduction/failure contract. No global prune or unrelated machine deletion.

Public-safe retention excludes raw verbose logs and secrets. Source checksums cover every tracked non-evidence file, including the updated documentation, and are relative to the repository root. Verify with `shasum -a 256 -c evidence/intended-install/packaged-linux/SOURCE_SHA256SUMS`. Historical runtime identity is separately bound by `result.json`'s source commit and exactly reproduced release-bundle digest.
