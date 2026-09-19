## ADDED Requirements

### Requirement: Collect only from sender endpoints
The operator's collector SHALL poll only sender `/stats` and `/receipts` endpoints,
persist observations locally, and expose CLI, JSON and CSV views.

#### Scenario: The customer device exposes no inbound management port
- **WHEN** the collector is started on the sender
- **THEN** it requires neither receiver SSH nor a receiver metrics listener
- **AND** it can retain observations while the customer's link is unavailable

### Requirement: Distinguish observation from acknowledgement
The CLI SHALL label bytes and requests as sender-observed, and staged/loaded state
as device-acknowledged. It SHALL NOT infer running or healthy from a successful send.

#### Scenario: All requested bytes were served but no acknowledgement arrived
- **WHEN** sender counters increase without a device receipt
- **THEN** delivery and runtime health remain UNKNOWN

### Requirement: Retry acknowledgements durably
The agent SHALL persist its acknowledgement before attempting outbound POST, keep
it after failure, and replay it during a later online reconciliation.

#### Scenario: The acknowledgement is accepted but the response is lost
- **WHEN** the same receipt is posted again
- **THEN** the sender retains one receipt keyed by the same content-derived ID

### Requirement: Handle counter reset and stale history
The collector SHALL preserve historical samples and use the origin epoch when
calculating response-body rates.

#### Scenario: The sender restarts
- **WHEN** a new sample has a different origin epoch
- **THEN** the collector does not calculate a rate across the reset boundary
- **AND** earlier samples and acknowledgements remain in the local database
