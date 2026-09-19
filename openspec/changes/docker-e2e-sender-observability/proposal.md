# Docker end-to-end demo and sender-side evidence

## Why
The first repository demonstrated transport with synthetic archives and made
Docker testing optional. Its operator examples inspected the receiver, which is
at a customer site. The user needs a simple real Docker demonstration, measured
data, sender-side CLI collection, editable architecture diagrams, and instructions.

## What Changes
- Make `make docker-demo` the main walkthrough: real builds, save, delta transfer,
  Docker load, and offline runtime verification of payload hashes.
- Include two images sharing real layers and a 32 KiB edit inside a large layer.
- Add a sender collector that persists HTTP observations and outbound device
  acknowledgements in SQLite, with live CLI and JSON/CSV export.
- Test acknowledgement retry and idempotency without an inbound receiver endpoint.
- Add native Excalidraw diagrams with SVG previews and plain-English README steps.
- Separate real Docker evidence from executed synthetic transport evidence.

## Capabilities

### New Capabilities
- `docker-e2e-demo`: executable real-image experiment and measured report.
- `sender-observability`: sender-local collection and acknowledged delivery state.
- `reference-documentation`: diagrams, instructions and reproducible evidence map.

### Modified Capabilities
None. Existing signed chunk-transfer and durability invariants remain unchanged.

## Impact
Go origin/agent gain optional outbound acknowledgement handling. Python gains a
real Docker test driver and a sender CLI collector. The existing Docker runtime
is unchanged. No Harbor installation, Kubernetes, Grafana or device metrics
listener is required. The acknowledgement endpoint is lab-only and unauthenticated;
customer deployment requires authentication and tenancy isolation before use.
