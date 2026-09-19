# Implementation and validation tasks

## 1. Real Docker experiment
- [x] 1.1 Implement real FROM-scratch builds with shared layers and a large payload.
- [x] 1.2 Change 32 KiB inside the existing payload layer and export both versions.
- [x] 1.3 Implement the throttling, corrupted/truncated response and SIGKILL/restart harness.
- [x] 1.4 Add Docker import, exact image-ID checks, and offline payload-hash probes.
- [x] 1.5 Produce sender-measured JSON/Markdown results and clearly label the computed baseline.
- [x] 1.6 Support distinct existing sender/receiver Docker contexts and record actual daemon IDs.
- [x] 1.7 Execute the real Docker test and retain PASSED_REAL_DOCKER evidence on a Docker host.
- [ ] 1.8 Repeat with the user's real 1-2 GiB images; compare with a measured registry pull.

## 2. Sender-side observability
- [x] 2.1 Implement sender-only collector, SQLite history, CLI view, JSON and CSV export.
- [x] 2.2 Keep origin observations separate from device staged/loaded acknowledgements.
- [x] 2.3 Add durable outbound receipt queue and idempotent sender receipt persistence.
- [x] 2.4 Test failed acknowledgement POST, queue retention, retry and duplicate handling.
- [x] 2.5 Test collector offline history, counter-epoch handling and UNKNOWN delivery state.
- [x] 2.6 Integrate sender collection into the measured transport harness.
- [ ] 2.7 Add authenticated per-device receipt attribution before customer deployment.

## 3. Documentation and diagrams
- [x] 3.1 Add three editable Excalidraw scenes and matching SVG previews.
- [x] 3.2 Make the primary README steps plain English with copyable commands.
- [x] 3.3 Add OpenSpec proposal, requirements, design, task list and structural checks.
- [x] 3.4 Preserve the original evidence separately from revision-2 measurements.
- [ ] 3.5 Run the official OpenSpec CLI strict validator on a host with that CLI installed.

## 4. Production follow-up (out of this reference demo)
- [ ] 4.1 Validate power-cut behavior on the target filesystem and storage hardware.
- [ ] 4.2 Add bounded cache/outbox retention, tenancy, credential rotation and enrollment.
- [ ] 4.3 Benchmark a maintained chunking engine and define format migration.
- [ ] 4.4 Design health-gated activation, rollback, and persistent-data compatibility.
