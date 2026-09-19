# Revision-3 execution record

Host execution 2026-09-18: macOS arm64 (Darwin 24), OrbStack Docker 29.4.0
(linux/amd64 daemons), Go 1.25.8, Python 3.11.15. This revision supersedes the
"real Docker NOT RUN" limitation recorded in `evidence/` and
`evidence/revision-2/`: the real Docker end-to-end demonstration has now been
executed and passed on a real Docker daemon. Earlier folders are retained
historical results, not assertions that their source hashes still describe the
current code; see `TESTED_SOURCE_SHA256SUMS` for the exact sources tested here.

## Platform scope of this historical record

These are **local Linux-VM/container results** from OrbStack, coordinated by macOS. The Docker engine's Linux platform does not establish that the hub and client were validated as long-running daemons on a standalone Linux host. The real Docker pass below remains valid within its recorded scope; it is not production or v3.1 sustainability proof. Linux-first daemon acceptance and the outstanding standalone-host requirement are described in [EXPERIMENTS.md](../../docs/EXPERIMENTS.md#linux-first-daemon-acceptance). No historical measurements are changed by this clarification.

## Real Docker end-to-end: PASSED (task 1.7)

`make docker-demo WORK=work/docker-validation-007 SIZE_MIB=16 RATE_KBIT=5000`
on OrbStack Docker 29.4.0 (Docker Engine API 1.5x, linux/amd64 sender and
receiver daemons on the macOS arm64 host). Status:
`PASSED_REAL_DOCKER` (`results.json`, `RESULTS.md`).

- Three FROM-scratch releases built by the sender daemon with a shared base
  layer and a 16 MiB payload (base 9.6 MiB + application 6.4 MiB):
  app-a-v1, app-b-v1 (different payload, same base), app-a-v2 (32 KiB edited
  inside the existing payload layer).
- Real throttled HTTP transfer at 5000 kbit/s aggregate response-body rate,
  with a mid-transfer SIGKILL of the receiving agent (exit code -9) after
  durable chunk commits: 5 chunks survived and were never requested again
  (`sigkill-before-resume.json`, `sigkill-after-resume.json`,
  `sigkill_preserved_chunks_re_requested: 0`).
- Chunk reuse across releases: app-b-v1 transferred 6,895,701 chunk body bytes
  instead of re-sending the 10 MiB shared layer; the 32 KiB app-a-v2 edit
  transferred 504,830 chunk body bytes. A repeated unchanged release made zero
  chunk requests and zero manifest bytes.
- Every release was reconstructed and imported into the receiver Docker
  daemon, then verified by `docker run` with `--pull never` and container
  networking disabled: the running probe re-hashed base and payload files and
  matched the build-side expectation exactly for all three releases.
- Signed image IDs are the preserved config digests; the source OCI index and
  manifest IDs were absent before load and are recorded separately as
  provenance. The loaded image was resolvable under its config digest
  (reference-type-aware check, fixed in this revision).
- Old and new images ran with the origin stopped and container networking
  disabled: payload verification of all releases (`results.json`:
  `offline_old_and_new_images_verified: true`).
- Sender-side collector observations and STAGED/LOADED acknowledgements are in
  `sender.sqlite` and `sender-report/`. LOADED acknowledges image import only;
  running/healthy is not inferred.

Build-cache context pitfall found and fixed on the way (retained attempts):

- `work/docker-validation-004/005`: a warm BuildKit COPY cache served the
  stale payload layer because successive contexts kept identical size and
  pinned mtimes; releases folded into one image.
- `work/docker-validation-006`: after adding `--no-cache`, the builds produced
  distinct config digests but identical layer digests — Docker's client-side
  context change detection also keys on size+mtime, so unchanged contexts
  re-sent the previous release's bytes. Root-caused by layer diffing of the
  retained archives; fixed by per-release pinned mtimes plus new assertions
  that reject any release whose config digest or layer digest list duplicates
  an earlier release (docker-validation-006 is retained as
  `FAILED_REAL_DOCKER_ATTEMPT` with its `failure.json`).
- `work/docker-validation-007` is the passing run retained here. `003/004/005`
  remain in the workspace as failed trials with their logs; they are excluded
  from this evidence bundle except as referenced above.

## Also executed in this revision

- `make check`: Go vet, Go unit tests, race detector, and Python tests passed
  after each fix and again after the sender cherry-pick (20 sender tests).
- `make docker-smoke SIZE_MIB=16 RATE_KBIT=5000` with the same
  context-change-detection fix: two FROM-scratch builds with distinct config
  and layer digests, transfer, import, runtime probe and zero-request repeat
  passed. Run directory `work/docker-smoke-450b86ffff04` retained in the
  workspace (excluded from this bundle by size).
- Sender fix `292c04f` ("Fix sender receipt ordering, freshness and full
  history exports") cherry-picked from the `sender-observation` workspace into
  this repository without conflicts; sender tests extended to 20 and passed.
- `make openspec-check`: fifteen requirements and three editable scenes passed
  local structural checks. The official OpenSpec CLI validator remains NOT RUN
  (CLI not installed on this host).
- Native .excalidraw sources and SVG previews unchanged from revision-2 and
  still structurally checked.

## Not run here (unchanged limitations)

- Physical power removal / abrupt device power-off: NOT TESTED. SIGKILL after
  durable commits is not a storage-durability claim.
- 1–2 GiB real user images: NOT BENCHMARKED in this revision (see the
  go/no-go note in the delivery result). The scripts accept those sizes.
- Remote/registry pulls: the baseline column remains a computed gzip size of
  missing whole layers, not a measured registry pull.
- Hosted CI: workflow included, NOT EXECUTED as part of this delivery.
- Authenticated per-device receipt attribution: still open (task 2.7).
