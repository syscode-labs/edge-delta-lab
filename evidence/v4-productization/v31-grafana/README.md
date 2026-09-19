# v3.1 Grafana telemetry evidence

Observed 2026-09-19 UTC against the OCI Grafana Cloud stack.

- Dashboard source: `deploy/helm/edgelab-hub/dashboards/edgelab-delivery-cache.json`
- Public reference: [dashboard guide and historical screenshot](../../../docs/GRAFANA.md)
- Datasource: `grafanacloud-prom` (resolved from `/api/datasources`)
- Import/readback: HTTP 200; version 2; 10 panels
- Query verification: all 18 dashboard targets plus `count({job="edgelab"})` (19 total) returned HTTP 200 and Prometheus `status=success`
- Cold range evidence: 16,791,337 bytes and 202 verified chunks
- Delta range evidence: 249,778 bytes, 3 verified chunks, and 202 reused chunks
- Integrity: zero-valued `edgelab_client_integrity_failures_total` series present; successful syncs reached 2; failed syncs stayed 0
- Series budget: 38 (hard ceiling 40)
- Post-completion scrapes: two exact query snapshots retained in `final-query-results.json`
- Screenshot/render: **NOT RUN**; no authenticated rendering path was available, so panel-query evidence is the acceptance artifact
- Remote-write credentials were not written to the repo or evidence

Publication note: private dashboard URLs and host labels have been replaced with
repository-local references and `remote-linux-host`; query timestamps and measurements
are unchanged. See [redaction and integrity](../../REDACTION.md). The historical
query count above predates removal of the rate target from the bytes-total panel.
The linked historical screenshot is included; **authenticated automated dashboard
capture/render verification** remains **NOT RUN**.

Machine-readable evidence:

- `preflight.json` — both exporters present, up/source_up=1, fresh samples, 38 series
- `cold-delta-range.json` — sanitized one-hour Grafana range retaining both cold and delta events
- `final-query-results.json` — dashboard readback and two post-completion scrapes
- `dashboard-metadata.json` — dashboard identity/readback metadata
- `credential-incident.json` — sanitized handling record; owner-controlled rotation remains recommended

The Alloy example is intentionally credential-free and uses an explicit `edgelab_*|up`
write allowlist.
