# Hosted v0.1.1 evidence index

The canonical interpretation, delivery status and limitations are in [TESTING.md](../../TESTING.md#hosted-v011-acceptance-and-v012-delivery-status).

Selected records from `work/usability-hosted-01` and `work/usability-hosted-download`, captured 2026-09-20:

- `result.json`: all 17 gate results, package identities, scope and cleanup.
- `summary-1.json`, `summary-2.json`, `summary-2-restart.json`: first cold/update and fresh restart completions.
- `anonymous-assets.json`, `SHA256SUMS`: original public URLs, HTTP status and asset hashes; the checksum manifest refers to published archives, not this directory's JSON files.
- `089.json`, `132.json`, `137.json`, `140.json`: runtime-key mode, receiver restart, key removal and surviving raw loopback hub probes.
- `158.json`, `159.json`, `161.json`, `162.json`: retained uninstall state checks and owned guest deletions.
- `closeout-verification.json`: original independent cleanup and full-run pattern-scan result. Its 170-file scan count describes the original run, not this selected subset.

Guest home paths are replaced with `$HOME`; measured values, timestamps, content hashes and disposable run identifiers are unchanged. Private keys, credentials, full logs, binaries, archives, caches and unrelated machine inventories are excluded. This is retained historical v0.1.1 evidence, not a rerun or v0.1.2 acceptance receipt.
