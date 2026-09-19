## ADDED Requirements

### Requirement: Notify through Telegram and Slack
The notifier layer SHALL deliver lifecycle events to Telegram (bot API sendMessage) and
Slack (incoming webhook) when configured, and SHALL accept secrets only from the
environment.

#### Scenario: A release is published
- **WHEN** the publish pipeline completes for a new digest
- **THEN** release-published events are delivered to every configured notifier
- **AND** no notifier secret appears in configuration files or event payloads

### Requirement: Cover the lifecycle event kinds
The notifier layer SHALL support the event kinds release-published, delivery-staged,
delivery-loaded, delivery-failed and watcher-error.

#### Scenario: A device fails verification
- **WHEN** a client reports a delivery failure
- **THEN** a delivery-failed event is fanned out to all configured notifiers with device and release identifiers

### Requirement: Retry with backoff and never block delivery
Notification failures SHALL be retried with exponential backoff (honoring Retry-After
for 429) and SHALL NOT block or abort the underlying delivery or publish path.

#### Scenario: Slack is briefly unavailable
- **WHEN** the webhook returns 500 then 200
- **THEN** the event is delivered exactly once after the retry
- **AND** the pipeline that emitted the event is unaffected
