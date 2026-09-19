## ADDED Requirements

### Requirement: Use real Docker images for the main demo
The main demo SHALL build real runnable Docker images, export them, deliver their
chunked archive, import through Docker, and verify runtime payload hashes.

#### Scenario: Docker is available
- **WHEN** the operator runs `make docker-demo` on a supported Docker host
- **THEN** the report includes image IDs, actual probe outputs and sender byte counts
- **AND** the command fails if the loaded image or either payload hash differs

#### Scenario: Docker is unavailable
- **WHEN** the Docker CLI or daemon cannot be used
- **THEN** the command exits unsuccessfully without substituting synthetic images
- **AND** no PASSED_REAL_DOCKER result is generated

### Requirement: Demonstrate intra-layer and cross-image reuse
The demo SHALL include two images with shared actual layers and a later 32 KiB
edit inside an existing large layer, not only an added small final layer.

#### Scenario: Deliver the changed application
- **WHEN** the device already has the older application's transport chunks
- **THEN** the changed version requests missing chunks only
- **AND** the report compares response-body data with computed missing-layer gzip bytes

### Requirement: Demonstrate interruption and repeat delivery
The demo SHALL inject real HTTP failures and kill the receiver process after
verified commits, then assert no completed chunk is fetched again on restart.

#### Scenario: Repeat a fully delivered release
- **WHEN** the exact same signed release is reconciled again
- **THEN** zero chunk requests and zero manifest response-body bytes are observed
- **AND** any conditional-request headers or acknowledgement traffic are not called zero wire traffic

### Requirement: Keep offline verification local
The demo SHALL stop the origin and run retained old and new images without network access.

#### Scenario: The WAN is unavailable after delivery
- **WHEN** the origin is stopped and containers run with pull disabled and no network
- **THEN** both old and new payload hashes match their build-time expected values
