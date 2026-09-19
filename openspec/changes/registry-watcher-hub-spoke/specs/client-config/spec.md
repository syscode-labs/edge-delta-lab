## ADDED Requirements

### Requirement: Configure the client through a file with explicit action modes
The client SHALL read a YAML configuration with origin URL, manifest URL, pinned public
key, state dir, optional cache dir, release allowlist/ignorelist, and action mode
`none` (default) | `load` | `restart`.

#### Scenario: Default is dry-run
- **WHEN** a config omits `action`
- **THEN** the client performs the full sync and verification and reports the actions it WOULD take without loading into Docker or restarting anything

### Requirement: Gate releases by allowlist and ignorelist
The client SHALL evaluate release eligibility by regex allowlist/ignorelist before any
download begins, ignoring ineligible channel sequences.

#### Scenario: An ineligible release is announced
- **WHEN** the desired channel names a release that does not match the allow rule or matches the ignore rule
- **THEN** the client takes no download action and records the skip

### Requirement: Load only after full verification
In `load` and `restart` modes the client SHALL import into Docker only after the
complete v2 verification chain (signature, whole-artifact hash, durable staging,
disk preflight) succeeds, and SHALL use local unix Docker endpoints only.

#### Scenario: Verification fails in load mode
- **WHEN** any verification step fails
- **THEN** no Docker command runs and the staged artifacts are preserved for diagnosis

### Requirement: Restart maps the new image to affected containers
In `restart` mode the client SHALL map the loaded image ID to running containers (by
image ID/label match) and recreate exactly those containers, recording each action.

#### Scenario: Two containers run the previous image ID
- **WHEN** the client loads a new image whose ID supersedes the one two running containers use
- **THEN** exactly those two containers are stopped, removed and recreated with the new image, and others are untouched

### Requirement: Cross-compile portable binaries
The build SHALL produce static (CGO_ENABLED=0) binaries for linux/amd64, linux/arm64
and darwin/arm64, and the client SHALL remain a single portable binary.

#### Scenario: Cross-compile targets build
- **WHEN** `make release-binaries` runs
- **THEN** all three target binaries link and pass `go vet` equivalents at build time
