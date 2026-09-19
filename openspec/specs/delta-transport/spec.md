# Delta transport specification

## Purpose
Deliver exact image archive bytes to an unreliable edge link without replacing
Docker, and without downloading reusable content again after an interruption.

## Requirements

### Requirement: Chunk before compression
The publisher SHALL apply content-defined chunking to exact uncompressed archive
bytes, identify raw chunks by SHA-256, compress each chunk independently, and sign
the ordered reconstruction recipe and expected image identities.

#### Scenario: A small edit changes a large layer
- **WHEN** 32 KiB changes inside an existing large file in an image layer
- **THEN** the edge reuses matching cached chunks and requests only missing chunks and metadata
- **AND** the report measures actual response-body bytes rather than assuming a saving

### Requirement: Preserve durable progress
The receiver SHALL verify chunk size and hashes, sync its temporary file, rename
it into the cache, and sync its directory before reporting a durable commit.

#### Scenario: The agent is killed during transfer
- **WHEN** the test sends SIGKILL after verified chunks were committed
- **THEN** restarting the agent reconstructs the exact target
- **AND** already committed chunks are not requested again

### Requirement: Verify before importing
The receiver SHALL verify the signed manifest, every chunk, and the full archive
before calling any Docker import operation.

#### Scenario: A response is corrupted
- **WHEN** the fault server returns a corrupted chunk body
- **THEN** the receiver rejects and retries that chunk
- **AND** the receiver never imports an unverified archive

### Requirement: Bound retransmission work
The transport SHALL retry incomplete chunk objects with bounded backoff while
retaining successfully verified content. It SHALL NOT silently fall back to a
full layer or full image download.

#### Scenario: The network cuts a chunk response short
- **WHEN** the response ends before its advertised content length
- **THEN** only that incomplete chunk is retried
- **AND** committed chunks remain available across process restarts
