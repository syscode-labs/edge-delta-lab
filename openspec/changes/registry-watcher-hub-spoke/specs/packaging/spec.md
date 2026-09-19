## ADDED Requirements

### Requirement: Provide a sender daemon container image
The repository SHALL provide a multi-stage container image containing the sender daemon
(and the client build target), running as non-root with declared volumes for origin,
state and admin socket, and a plain docker-run path documented.

#### Scenario: Run the hub from the published-image-equivalent local build
- **WHEN** the image is built locally and run with a mounted origin and state directory on the local OrbStack daemon
- **THEN** `/healthz` answers 200 and a client can sync a published release from the container

### Requirement: Provide a Helm chart with lint and template gates
The repository SHALL provide a Helm chart for the sender daemon with values for image,
persistence, admin socket, notifier secrets, and resources, gated by `helm lint` and
`helm template` smoke tests.

#### Scenario: Chart renders a deployable manifest set
- **WHEN** `helm template` runs against default values
- **THEN** Deployment, Service and PVC manifests render without errors
- **AND** `helm lint` passes

### Requirement: No cluster deployment claims without execution
Packaging evidence SHALL distinguish executed container smoke runs from unexecuted
cluster deployment, which SHALL be recorded NOT RUN with a reason.

#### Scenario: Reporting packaging status
- **WHEN** PRODUCT_RESULT.md reports the packaging stage
- **THEN** helm lint/template and local container smoke results are labeled with what actually executed
