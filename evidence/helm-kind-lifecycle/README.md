# Real signed Kind Helm lifecycle verification

**PASS**, executed 2026-09-19 UTC (2026-09-20 local session date). This is a real single-node Kubernetes lifecycle and signed-delivery test, not Helm rendering or a mocked API. The retained run used the chart at pre-amendment commit `8d9ccc819579b620e79d4bfffc3bfe324d0a3c29` plus the signed-delivery harness extension included in this same amended change. No chart source changed after that run.

## Environment and reproduction

- macOS host, local OrbStack Docker 29.4.0, Linux amd64 containers.
- Kind v0.32.0; Kubernetes server v1.36.1; default `standard` local-path storage class.
- Signed lifecycle: Helm v4.2.3. Closeout lint/render checks: Helm v3.14.1.
- Real `Dockerfile` daemon target built from the checkout and loaded into Kind; hub, exporter and proof client use `imagePullPolicy: Never`.
- Private temporary kubeconfig; unique owned cluster/image names. No production cluster access, application Docker socket mount, global prune, publication or push.

From the repository root, with Go, Python, Docker, Kind, Helm and kubectl available:

```sh
python3 scripts/helm_kind_lifecycle.py --evidence work/helm-kind-run
python3 scripts/helm_contract_test.py
```

Use a new evidence directory each time. The cluster/image are removed in `finally`, and the host temporary directory (including signing keys and kubeconfig) is removed by `TemporaryDirectory`. Evidence is retained.

## Defects found and fixed

1. **Origin PVC never bound.** The original chart requested `ReadOnlyMany`. The local-path provisioner rejected it: `NodePath only supports ReadWriteOnce and ReadWriteOncePod (1.22+) access modes`. Changed the origin claim to `ReadWriteOnce`; the hub's `/origin` mount remains read-only. Existing bound claims need a backed-up migration, not an immutable-field upgrade.
2. **Kubelet refused the container.** After the PVC fix, `runAsNonRoot` plus image `USER edgelab` produced `CreateContainerConfigError`: kubelet could not verify a non-numeric image user. Added default `podSecurityContext.runAsUser: 100`, matching the supplied image, without weakening other controls.

Original Helm errors and Kubernetes events are retained in [initial-failures](initial-failures/).

### Concise superseded attempt notes

- Original run 1 failed because the local Python lacked `shlex.join`; quoted joining replaced it. No cluster created.
- Original run 2 checked the event log before the first 10-second stats snapshot. The harness now waits up to 30 seconds for a real event; that failed run's cluster/image cleanup succeeded.
- Original run 3 passed the empty-origin lifecycle, now superseded by the strictly broader signed run retained below.
- Signed run 1 failed before cluster creation because Go PATH/GOROOT selected mismatched tool versions (1.26.0 and 1.25.8). Corrected the environment.
- Signed run 2 failed before cluster creation because `--size-mib 1` is unsupported; switched to the supported 2 MiB fixture. The image-removal command reported absent images in both pre-build failures, not leaked resources.
- An earlier full check used an old background-shell Python and failed compatibility tests. Closeout reruns the full suite with Go 1.25.8 and Python 3.11.15; see validation below.

Empty/noisy failed-run directories and the redundant empty-origin passing transcript were removed. These notes preserve their diagnostic value.

## Passing signed result

[signed-run-3/result.json](signed-run-3/result.json) is the machine-readable verdict; [signed-run-3/transcript.log](signed-run-3/transcript.log) records commands/output through successful uninstall and cleanup.

| Gate | Observed result |
| --- | --- |
| Fresh install | Ready; both PVCs Bound; Service health; UID 100; admin socket and real event log; origin writes rejected |
| Signed publication | Host-only signing key; only published release/chunk objects copied into origin PVC by a temporary non-root loader; hub remains read-only |
| Real watch client | Public-key-only ConfigMap; no service-account token; `phase=staged`, sequence 1, release `signed-lifecycle`; reconstructed archive SHA-256 and size match host |
| Archive identity | `eb3f673fd08a5906154808290c5c964dd2d40da8178c7d556cd7647e552e01ea`, 2,104,832 bytes |
| Origin persistence | Every published object's SHA-256 checked over the Service after population, upgrade, restart and rollback |
| Helm upgrade | Exporter Service metrics `source_up=1`; events disabled/rate 7500; same PVC UIDs, state marker and original event-log prefix |
| Pod restart | New pod UID; same PVC UIDs, marker, event-log prefix and origin bytes |
| Helm rollback | Revision-1 settings restored; exporter removed; events enabled/rate zero; same persisted state and origin bytes |
| Helm uninstall | No release deployment, pod, Service, PVC or Helm secret remained |
| Cleanup | Owned cluster/image deleted; independent closeout found no Kind cluster, owned container/image, lifecycle process or temporary kubeconfig directory |

[validation.json](validation.json) records actual closeout checks: focused harness tests, compilation, Helm lint, nine render combinations plus real Docker runtime contracts, serial full `make check`, repository OpenSpec check, and official OpenSpec 1.6.0 strict validation of both changes.

## Evidence privacy

No key material, kubeconfig contents, bearer credentials or tokens are retained. The `publisher.key` command argument names a host-only temporary file, not its contents. Public-key-only client volumes and the origin allowlist were reviewed in the harness. Home/repository/temporary paths are placeholders; the disposable node address and Docker context storage identifier were additionally redacted at closeout. Disposable cluster Pod/Service addresses and resource UIDs remain as lifecycle evidence, not production host details.

## Boundaries

This proves **signed synthetic archive staging and chart configuration lifecycle on single-node Kind**. It does not prove Docker activation, image-version upgrades, external notifications, production CSI permissions, multi-node/high availability, physical power-loss durability, or arm64 execution. Upgrade/rollback use the same source-built daemon image.

**Uninstall deletes both chart-managed PVCs.** With local-path's Delete reclaim policy this is destructive, not retention. Back up real deployments first. Persistence is verified only through upgrade/restart/rollback. Alternative images/storage drivers may need explicit security/ownership configuration. The six unfinished production-readiness tasks remain unchecked.
