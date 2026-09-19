# Revision-2 execution record

This folder is new evidence for the updated repository. The original `evidence/`
files are retained historical results, not assertions that the old source hashes
still describe the changed code.

## Executed

- `make check`: Go vet, Go unit tests, race detector, and seven Python tests passed.
- `make openspec-check`: fifteen requirements and three scene files passed the
  local structural checks. This is not the official OpenSpec CLI validator.
- `scripts/watch_smoke.py`: one agent process staged two successive releases.
- `scripts/demo.py --size-mib 16 --rate-kbit 5000`: actual HTTP transfers with
  throttling, response cuts, corruption, HTTP errors, SIGKILL/restart, cross-image
  reuse, a 32 KiB intra-layer edit, repeated release, insertion, version skipping,
  cache repair and offline reconstruction. All assertions passed.
- Sender collector ran against the sender endpoints, saved SQLite observations,
  and collected outbound STAGED acknowledgements. It did not inspect receiver
  files or call a receiver metrics endpoint. The test orchestrator itself does
  inspect local cache files to assert SIGKILL recovery; that is test control,
  not the sender collector's data source.
- Durable receipt retry/idempotency was exercised by a Go test with a real HTTP
  server: failed POST retained the receipt, retry succeeded, duplicate remained
  one stored receipt. This is not a Docker import execution.

## Not executed

The new `scripts/docker_demo.py` was invoked but exited before any build because
no Docker CLI/daemon is available. `docker-attempt.txt` retains that result.
No real Docker build/import/run success is claimed. Run `make docker-demo` on a
Docker test host to produce that evidence. The included CI workflow has not
been remotely executed in this session. Two-daemon Docker execution, actual
1-2 GiB production-image benchmarks, power-cut tests, official OpenSpec CLI
validation and Excalidraw browser import are also pending.

## Files

`RESULTS.md` and `results.json` contain the measured transport run. `sender.sqlite`
is a consistent SQLite backup of the collector's database. `sender-report/`
contains readable text, JSON and CSV exports; `receipts/` contains sender-retained
STAGED acknowledgements. `checks.txt` and `transport-console.txt` record commands.
`validation-status.json` makes the distinction machine-readable.
`TESTED_SOURCE_SHA256SUMS` identifies the source included in this revision.

## Read it from the sender CLI

```sh
python3 scripts/sender.py status --db evidence/revision-2/sender.sqlite
```

The displayed reachability and rate are historical observations. The origin is
stopped after the test. A counter reflects response-body bytes written by the
sender, not TCP/IP wire bytes or durable delivery. The comparison baseline is
a computed gzip size of missing synthetic layers, not a measured registry pull.
