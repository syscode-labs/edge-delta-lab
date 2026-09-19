## ADDED Requirements

### Requirement: Expose an admin unix socket with JSON request/response
The daemon SHALL expose a local admin endpoint on a unix socket (newline-delimited
JSON request/response) answering stats, clients, receipts, events and watch-status
queries, created 0660 and removed on shutdown.

#### Scenario: An operator queries live stats
- **WHEN** a client sends `{"cmd":"stats"}` to the admin socket
- **THEN** the daemon replies with one JSON document of current hub stats

### Requirement: TUI is a separate binary over the admin socket
The TUI SHALL be a separate `edgelab-tui` binary that connects to the admin socket
path, renders live hub stats, per-client table, recent receipts and watcher status, and
SHALL also provide non-TTY JSON and CSV one-shot export in the same binary.

#### Scenario: Observing without a terminal
- **WHEN** `edgelab-tui --socket path --json stats` runs in a pipeline
- **THEN** it prints the JSON document from the socket and exits, without requiring a TTY

### Requirement: The daemon keeps a persistent event log
The daemon SHALL append lifecycle events (publish, promote, announcements, deliveries,
notifier and watcher failures) to a size-capped, fsynced JSONL log usable by the admin
socket, replacing the observability role previously filled by the Python collector.

#### Scenario: The daemon restarts
- **WHEN** the daemon restarts
- **THEN** previously logged events remain queryable through the admin socket
