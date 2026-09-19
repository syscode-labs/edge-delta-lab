# Publication redaction and evidence integrity

This public tree retains historical measurements, not a new benchmark run.
Personal account identity, machine labels, private endpoints, workstation paths and
private access descriptions have been redacted across text and SQLite evidence.

- `$REPO` denotes the repository root; `$HOME`, `$USER` and `$TMPDIR` replace local
  user and temporary-directory identifiers. These are placeholders in historical
  records, not literal runtime paths or claims that those locations still exist.
  Historical command `PATH` fields retain the Go/Python toolchain prefixes but
  replace unrelated workstation entries with `$REDACTED_PATH`.
- `remote-linux-host` replaces private host labels. `192.0.2.20` is a
  documentation-reserved replacement, not a reachable test endpoint.
- Private dashboard URLs now point to the [public Grafana guide](../docs/GRAFANA.md),
  its unchanged historical screenshot and retained query evidence. Host-label
  replacements preserve joins between query results and the Alloy example.
- SQLite was updated through SQLite APIs with secure deletion and vacuuming.
  All rows and non-path values were compared with the original data; measurements,
  receipt identifiers, timestamps and artifact hashes are preserved. Historical
  receipt identifiers identify the original records, not hashes of redacted bodies.
- The historical capture helper now uses the caller's toolchain and Docker context
  instead of hard-coded workstation locations. Review newly captured logs for
  privacy before publishing them; the helper is not an automatic sanitizer.

`PUBLIC_REDACTED_SHA256SUMS` is the public checksum index of modified evidence files
(excluding this note and the index itself). It identifies the redacted copies, not
original capture bytes. Existing `TESTED_SOURCE_SHA256SUMS`, `source_sha256` maps
and source commit identifiers remain historical provenance; they must not be
rewritten to imply that the current source was used for an earlier test. Existing
artifact/content hashes and signed manifests remain unchanged.

Some historical source fingerprints describe intermediate source snapshots not
retained as Git blobs: 15 of 254 entries across the source-checksum lists and
command records could not be independently recovered from the local history.
Those fingerprints are left intact, not replaced with current-source hashes.
Two historical `.out.json` files are empty (a killed transfer and a command with
no stdout); four `.jsonl` logs also contain Docker's plain-text `Loaded image:`
line. These original capture formats are preserved, not fabricated into JSON.

## Publication scope

Sanitizing this tree does **not** sanitize its parent commits or Git author metadata.
Do not publish the existing development history as-is. Publish a fresh reviewed
snapshot with a public-safe commit identity, or separately sanitize and verify all
history and refs intended for publication. No history rewrite or remote publication
is established by this remediation.
