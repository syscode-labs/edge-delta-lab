## ADDED Requirements

### Requirement: Watch any Docker Registry v2 endpoint
The watcher SHALL poll tags and manifests over the Docker Registry v2 HTTP API
(distribution spec) and SHALL NOT require Harbor-specific endpoints. It SHALL support
bearer-token auth per the distribution spec.

#### Scenario: Harbor-compatible token auth
- **WHEN** an anonymous request to `/v2/` returns 401 with a `WWW-Authenticate: Bearer realm="...",service="..."` challenge
- **THEN** the watcher obtains a bearer token from the realm with the requested scope and retries with `Authorization: Bearer ...`
- **AND** tag listing and manifest head/GET succeed without Harbor-specific calls

### Requirement: Filter tags by allowlist and ignorelist
The watcher SHALL apply per-repository regex `allow` and `ignore` patterns where a tag
is eligible only if it matches `allow` (when set) and does not match `ignore`.

#### Scenario: Ignored tag wins over allow
- **WHEN** tag `v1-canary` matches the configured allow `^v.*` and the ignore `.*-canary$`
- **THEN** the watcher does not emit a publish request for it

#### Scenario: Allowlist empty means all tags eligible
- **WHEN** no `allow` pattern is configured for a repository
- **THEN** every non-ignored tag is eligible

### Requirement: Detect publication by manifest digest change
The watcher SHALL resolve each eligible tag to a manifest digest and emit a publish
request `repo:tag@digest` when the digest differs from last-seen, including first-seen.

#### Scenario: A tag is re-pushed with new content
- **WHEN** the manifest digest for an eligible tag changes between polls
- **THEN** the watcher emits a publish request carrying the new digest
- **AND** no request is emitted when the digest is unchanged

### Requirement: Persist last-seen digests durably
The watcher SHALL persist observed tag→digest state and SHALL NOT re-emit a publish
request for unchanged digests across restarts.

#### Scenario: The watcher restarts
- **WHEN** the process restarts with unchanged registry content
- **THEN** no publish requests are re-emitted for previously observed digests

### Requirement: Trigger the publish pipeline for a new digest
The watcher SHALL trigger the existing signed publish pipeline for a new digest and
SHALL NOT weaken signature, hash or durable-commit semantics on that path.

#### Scenario: A new image is published to the registry
- **WHEN** the watcher detects digest `sha256:...` for an allowed tag
- **THEN** the registry content is exported to an archive, chunked, signed and promoted through the v2 publish path unchanged

### Requirement: Watcher errors are observable
The watcher SHALL record poll, auth and trigger failures as watcher-error events
surfaced to notifiers and the admin event log without stopping the poll loop.

#### Scenario: The registry endpoint goes away
- **WHEN** consecutive polls fail with connection errors
- **THEN** the watcher logs watcher-error events with backoff and resumes polling when the endpoint returns
