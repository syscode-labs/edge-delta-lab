## ADDED Requirements

### Requirement: Serve many concurrent clients from one sender
The hub SHALL serve concurrent clients with singleflight chunk reads so that
simultaneous requests for the same object produce one underlying read.

#### Scenario: Fifty clients request the same cold chunk
- **WHEN** 50 clients concurrently GET the same uncached object
- **THEN** exactly one underlying read serves the coalesced requests
- **AND** every client receives byte-identical verified content

### Requirement: Cache hot chunks within a byte budget
The hub SHALL cache hot encoded chunks in a bounded LRU with a configurable byte
budget and SHALL evict least-recently-used entries when the budget is exceeded.

#### Scenario: The cache budget is exceeded
- **WHEN** cached bytes exceed the configured budget
- **THEN** least-recently-used entries are evicted
- **AND** cached bytes never exceed the budget

### Requirement: Track per-client served metrics
The hub SHALL attribute served requests and bytes per client identity (device header)
and expose per-client metrics through the admin socket.

#### Scenario: Two devices pull different volumes
- **WHEN** device A pulls 1 MiB and device B pulls 2 MiB
- **THEN** per-client metrics report the served bytes per device independently

### Requirement: Preserve v2 transport semantics
The hub SHALL preserve v2 response semantics (ETag/304, range behavior, injected-fault
compatibility) and SHALL NOT weaken any v2 verification, durability or fault-injection
behavior.

#### Scenario: A legacy v2 client polls desired.json
- **WHEN** a v2 client GETs the channel manifest with an ETag
- **THEN** the hub answers 304 on unchanged content exactly as the v2 origin did
