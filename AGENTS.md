# Development constraints

This repository is a runnable experiment, not a production-ready updater. Preserve explicit evidence boundaries.

Run `make check`, `python3 scripts/watch_smoke.py`, and a fresh `scripts/demo.py --work ...` after transport changes. Run `make docker-smoke` on a real test Docker daemon before claiming Docker end-to-end verification. A CI configuration is not proof of a CI execution.

Never weaken signature, exact-size, encoded-hash, raw-hash or whole-artifact checks to make an interrupted transfer succeed. Never update Docker before full verification. Never replace content-addressed reuse with fixed-offset block reuse and keep claiming insertion resynchronization. Never call this a desync implementation or wire-compatible format.

A successful chunk commit requires file sync, atomic rename and directory sync. The state journal is not evidence that a chunk exists. Revalidate content on recovery. Never silently fall back to a full image transfer. Disk errors are not reasons to repeatedly redownload.

Keep the Docker socket out of the simulation containers. The explicit host Docker importer is privileged; do not auto-enable it. Preserve active and rollback artifacts when designing GC. Loading is not activation and image rollback is not database rollback.

Next priorities: real-image benchmark and real Docker test; power-loss/storage-failure harness; maintained CDC engine evaluation; compact/per-layer indexes and no-op fast path; bounded cache/GC; authenticated enrollment and trust rotation; health-gated activation and signed rollback intent. Update evidence and limitations alongside code.


## Revision-2 contract
Read openspec/config.yaml and the active docker-e2e-sender-observability change
before changing behavior. Main acceptance command is `make docker-demo`; the
synthetic demo is not Docker proof. Sender.py must never SSH/scrape/read receiver
state. Receipts are outbound only; served bytes are not acknowledged progress.
Keep LOADED distinct from RUNNING/HEALTHY. Do not infer trust from unauthenticated
lab receipts. Do not mark task 1.7 complete until retained real Docker results exist.
Native .excalidraw sources and SVG previews must agree. Keep README instructions
plain English and runnable without access to the original chat.
