# Local packaged mTLS evidence — 2026-09-20

**PASS:** `work/usability-local-08`, 17 gates, no cleanup errors, no retained guests.
Locally rebuilt v0.1.1 Linux amd64 package; **not hosted-release acceptance**.
Source: `4a8c5cc60eeb00f73b512c7348f2ca853c8e1a50` plus the installer/harness
repair fingerprinted below. This directory is a dated receipt, not proof of later
source or package bytes. No remote publication is established here.

[TESTING.md](../../TESTING.md#new-usability-and-mtls-acceptance) owns the human
results, security-repair explanation, scope/limitations and
[reproduction instructions](../../TESTING.md#packaged-installation-acceptance-reproduction).

## Raw evidence index

- [result.json](result.json): 17 gates, package/binary identities, architecture
  scope and cleanup result; copied from the final local attempt.
- [summary-1.json](summary-1.json), [summary-2.json](summary-2.json),
  [summary-2-restart.json](summary-2-restart.json): cold, update and restart.
- [lifecycle-probes.json](lifecycle-probes.json): effective systemd overrides,
  private runtime key metadata, stop/uninstall retention and owned-guest deletion.
- [installed-receiver-bytes.json](installed-receiver-bytes.json): installed
  binary/manager byte comparison.
- [original-0440-failure.json](original-0440-failure.json): earlier real systemd
  credential-permission failure, not the final acceptance result.
- [source-sha256.json](source-sha256.json): repaired source fingerprints at capture;
  historical hashes are not rewritten for subsequent documentation edits.
- [make-check.txt](make-check.txt): completed fresh regression output from
  `work/credential-regressions-final.txt` (Go vet/tests/race, 82 Python tests,
  3 Compose tests). Only workstation temporary-directory prefixes are replaced
  with `$TMPDIR`; test results and measurements are unchanged.

Selected evidence contains metadata and public identities, not private PEM,
enrollment keys or registry passwords. Disposable guest paths/IDs are retained;
workstation temporary paths are sanitized. Full work directories are intentionally
excluded. See the [redaction policy](../REDACTION.md).
