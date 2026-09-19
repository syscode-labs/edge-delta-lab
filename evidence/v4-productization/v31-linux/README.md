# Reproduce v3.1 Linux daemon measurements

This measures transport daemons, not Docker image import/activation. Fixtures are the
repository's deterministic 16 MiB synthetic Docker archives. The actual run used the local
OrbStack Linux VM; a native Linux Docker engine can run the same harness without changing
product code. The script refuses non-Linux execution.

From the repository root, with Go and a Linux container engine:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/release/edgelab-linux-amd64 ./cmd/edgelab
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/release/edgelab-linux-arm64 ./cmd/edgelab
# Select the binary matching the container architecture; this run was amd64.
docker run -d --name edgelab-v31-linux --cap-add NET_ADMIN alpine:3.20 sleep 7200
trap 'docker rm -f edgelab-v31-linux' EXIT
docker exec edgelab-v31-linux apk add --no-cache python3 iproute2
docker cp bin/release/edgelab-linux-amd64 edgelab-v31-linux:/tmp/edgelab
docker cp scripts/linux_sustainability.py edgelab-v31-linux:/tmp/linux_sustainability.py
docker exec edgelab-v31-linux python3 /tmp/linux_sustainability.py
mkdir -p work/v31-results
docker cp edgelab-v31-linux:/tmp/results/. work/v31-results/
```

Do not use host networking or mount a Docker socket. NET_ADMIN is needed only for the
container network namespace's loopback qdisc. The harness reaps its process children,
removes its generated fixtures/keys/cache directory and deletes its qdisc in `finally`.
The enclosing trap removes the whole container even on interruption. Use an unused name;
never delete a pre-existing container you do not own. Run test suites serially.

## Measured topology and boundaries

- Idle: one Linux `serve --hub --events`, three Linux `watch --events-url` clients
  (one-hour fallback polling), one Linux `watch` poll client (30 seconds); 600 seconds
  after all four have completed initial convergence. Sampling confirms daemon liveness.
- Link: one Linux hub and one continuously running Linux watch client; promote three
  releases without restarting the client. In-process rate limiter disabled. Linux
  `tc netem rate 5mbit delay 100ms` shapes loopback in both directions. Approximate RTT
  is therefore 200 ms, not 100 ms. Control/stats traffic also shares this queue.
- Soak: one Linux hub and one continuously running watch client; the same kernel qdisc
  plus a 1,000-kbit/s application pacer so two 30-second HTTP-503 periods interrupt
  outstanding work. This is server-unavailability injection, not packet blackholing.
  Pre-existing raw cache files are size/hash verified and mapped through the manifest
  to exact encoded object paths; their request counters must not increase.
- Only request/response-body bytes are counted by product counters. They exclude
  headers, WebSocket pings, TCP retransmission and VPN traffic. Qdisc counters include
  additional control traffic and are not an isolated VPN overhead measurement.
- No standalone Linux deployment or Linux Tailscale transfer was possible with the
  existing access. See `../v31-access/README.md`.

## Evidence review correction

A read-only review caught a raw-versus-encoded hash mismatch in the initial soak
assertion before its result was accepted. The initial harness was interrupted after
retaining the valid idle/link stages. The corrected soak stage was run separately
with exact manifest-derived paths, verified cache contents, nonempty observed request
mappings, exercised HTTP-503 counters, retry attempt/delay observations, and daemon
liveness. Do not reuse the initial soak assertion. Retained final soak evidence comes
only from the corrected run. The current script includes those checks.
