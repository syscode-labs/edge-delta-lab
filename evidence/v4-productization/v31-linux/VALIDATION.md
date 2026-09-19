# v3.1 validation record

Product source revision: `1125826` (new measurement scripts/docs do not change Go runtime code).

- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath ... ./cmd/edgelab`: exit 0.
- `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath ... ./cmd/edgelab`: exit 0.
- `file` identifies both as statically linked Linux ELF binaries with the matching architectures;
  exact SHA-256 values are in `artifacts.txt`.
- amd64 artifact executed throughout the Linux daemon measurements; arm64 runtime execution
  **NOT RUN**. Cross-compilation is not arm64 runtime proof.
- Serial `make check`: exit 0. `go vet`, ordinary Go tests, 35 Python tests and race-enabled
  Go tests passed. This suite ran on the coordinating workstation and is regression evidence,
  not the Linux sustainability runtime evidence. Only one suite invocation ran at a time.
- `python3 scripts/verify_sustainability.py` validates retained measurement counters and
  computes `verification.json`; it does not convert missing external/VPN stages into PASS.

The earlier anomalous `scripts/sender_test.py` failure came from two overlapping background
suite invocations in one checkout. Isolated/serial foreground `make test` and `make check`
passed. This is a test-harness execution artifact, not sender/client daemon behavior.
Those execution labels describe the test runner only, not the product runtime.
