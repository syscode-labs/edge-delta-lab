# Measured demo results

Fixture payload: 16 MiB per image; edit: 32 KiB; link: 5000 kbit/s.

| Scenario | Chunk response body bytes | Metadata body bytes | Seconds |
|---|---:|---:|---:|
| cold-sigkill-and-resume | 17,072,868 | 53,581 | 30.286 |
| second-image-shared-base | 6,819,307 | 54,877 | 11.396 |
| small-edit-in-large-layer | 249,778 | 53,581 | 0.572 |
| unchanged-repeat | 0 | 0 | 0.053 |
| inserted-bytes-resynchronize | 253,874 | 53,581 | 0.574 |
| skip-v1-directly-to-v3 | 262,066 | 53,581 | 0.600 |
| repair-one-corrupt-cache-chunk | 76,731 | 0 | 0.208 |

Verified chunks preserved across SIGKILL: 9; re-requested: 0.
Offline reassembly completed with zero HTTP requests.

## Measurement limits

- All transport is actual HTTP on loopback, paced at the configured aggregate body rate.
- Server byte counters count body bytes accepted by its socket writer, not TCP/IP wire bytes.
- Cold row includes the killed process; its agent_summary covers only the resumed invocation.
- The native baseline is computed gzip size of missing fixture layers, not a measured registry pull.
- Fixtures are high-entropy synthetic layered archives, not a representative production image corpus.
- SIGKILL is tested. Actual device power loss and Docker import are not tested by this script.
