## ADDED Requirements

### Requirement: Push-only websocket announcements
The hub SHALL expose a websocket endpoint `/events` that pushes announcement messages
when a signed release is published or promoted, and SHALL NOT carry chunk or manifest
bytes over that channel.

#### Scenario: A publish completes while a client is connected
- **WHEN** the hub promotes a new release to the desired channel
- **THEN** every connected client receives an announce message naming the channel and sequence within one second

### Requirement: Clients reconnect with backoff and fall back to polling
The client push dialer SHALL reconnect with exponential backoff and SHALL fall back to
HTTP polling of `releases/desired.json` with the existing ETag/304 logic when the
websocket cannot be established or held.

#### Scenario: The hub restarts
- **WHEN** the websocket drops
- **THEN** the client continues to converge by polling on its existing interval and redials with backoff until the hub returns

### Requirement: Announcements carry no trust
Announcements SHALL be treated as hints only; clients SHALL verify the signed channel
manifest before acting, and an announcement SHALL NOT by itself cause any state change.

#### Scenario: A forged announce arrives
- **WHEN** a client receives an announce for a release the signed channel does not name
- **THEN** the client ignores it after manifest verification fails or does not name it
