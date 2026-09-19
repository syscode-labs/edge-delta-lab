# Real Docker end-to-end results

Status: **PASSED_REAL_DOCKER**. Limit: 5000 kbit/s.

| Release | Chunk body bytes | Manifest body bytes | Computed missing-layer gzip bytes | Docker run |
|---|---:|---:|---:|---|
| app-a-v1 | 19,051,572 | 61,701 | 18,549,165 | Payload hashes verified |
| app-b-v1 | 6,895,701 | 63,269 | 6,713,231 | Payload hashes verified |
| app-a-v2 | 504,830 | 61,701 | 6,713,229 | Payload hashes verified |

5 chunks survived SIGKILL without another request. Unchanged release: zero chunk requests.
Old and new images ran with the origin stopped and container networking disabled.

origin HTTP response-body bytes; excludes headers, TCP/TLS overhead and outbound receipts
computed gzip size of missing whole layers; not a measured ordinary registry pull

Topology: same-daemon local integration. Source config ID, source index ID and tag were absent before load.
Signed image IDs are preserved config digests; source OCI index/manifest IDs are recorded separately as provenance.
